<script lang="ts">
	import { onMount, onDestroy } from 'svelte';

	interface Props {
		sessionId: string;
		onClose: () => void;
	}

	let { sessionId, onClose }: Props = $props();

	// --- Types ---
	type PipelineState = 'idle' | 'listening' | 'transcribing' | 'thinking' | 'speaking' | 'error';
	type Mode = 'assistant' | 'relay'; // assistant = haiku answers; relay = inject to orchestrator

	interface Turn {
		role: 'user' | 'assistant';
		text: string;
	}

	// --- State ---
	let pipelineState = $state<PipelineState>('idle');
	let mode = $state<Mode>('assistant');
	let autoLoop = $state(false);
	let history = $state<Turn[]>([]);
	let errorMessage = $state('');
	let vadReady = $state(false);
	let historyEl = $state<HTMLDivElement | null>(null);

	// --- Audio / VAD internals (not reactive) ---
	let audioCtx: AudioContext | null = null;
	// Tracks active AudioBufferSource nodes so we can stop them on barge-in
	let activeSources: AudioBufferSource[] = [];
	// Abort controller for in-flight TTS fetch
	let ttsAbort: AbortController | null = null;
	// VAD instance
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let vad: any = null;
	let errorTimer: ReturnType<typeof setTimeout> | null = null;

	// --- Lifecycle ---
	onMount(async () => {
		await initVAD();
	});

	onDestroy(() => {
		stopEverything();
		vad?.destroy?.();
		if (errorTimer) clearTimeout(errorTimer);
	});

	// --- AudioContext (unlock on first use for iOS) ---
	function ensureAudioCtx(): AudioContext {
		if (!audioCtx) {
			// eslint-disable-next-line @typescript-eslint/no-explicit-any
			const Ctx = window.AudioContext || (window as any).webkitAudioContext;
			audioCtx = new Ctx({ sampleRate: 24000 });
		}
		return audioCtx;
	}

	// --- VAD init ---
	async function initVAD() {
		try {
			const { MicVAD } = await import('@ricky0123/vad-web');
			vad = await MicVAD.new({
				positiveSpeechThreshold: 0.5,
				negativeSpeechThreshold: 0.35,
				redemptionFrames: 10,     // ~300ms of silence before speech ends
				preSpeechPadFrames: 2,
				minSpeechFrames: 3,
				workletURL: '/vad.worklet.bundle.min.js',
				modelURL: '/silero_vad_v5.onnx',
				ortConfig: (ort: unknown) => {
					// eslint-disable-next-line @typescript-eslint/no-explicit-any
					(ort as any).env.wasm.wasmPaths = '/';
				},
				stream: await navigator.mediaDevices.getUserMedia({
					audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true }
				}),

				onSpeechStart: () => {
					// Barge-in: user started speaking — stop any playing TTS immediately
					if (pipelineState === 'speaking') {
						stopTTSPlayback();
					}
				},

				onSpeechEnd: (audio: Float32Array) => {
					// audio: Float32Array at 16kHz from VAD
					if (pipelineState === 'listening') {
						pipelineState = 'transcribing';
						runPipeline(audio);
					}
				},
			});
			vadReady = true;
		} catch (err) {
			setError('Mic access denied or VAD failed to load');
		}
	}

	// --- Controls ---
	function startListening() {
		if (!vadReady || pipelineState !== 'idle') return;
		// Unlock AudioContext on first user gesture (iOS requirement)
		const ctx = ensureAudioCtx();
		if (ctx.state === 'suspended') ctx.resume();
		vad?.start();
		pipelineState = 'listening';
		errorMessage = '';
	}

	function stopListening() {
		vad?.pause();
		if (pipelineState === 'listening') pipelineState = 'idle';
		autoLoop = false;
	}

	function stopTTSPlayback() {
		activeSources.forEach((s) => { try { s.stop(0); } catch (_) {} });
		activeSources = [];
		ttsAbort?.abort();
		ttsAbort = null;
	}

	function stopEverything() {
		stopListening();
		stopTTSPlayback();
		audioCtx?.close();
		audioCtx = null;
	}

	function handleMainButton() {
		if (pipelineState === 'listening') {
			stopListening();
		} else if (pipelineState === 'idle' || pipelineState === 'error') {
			startListening();
		}
		// no-op during transcribing/thinking/speaking
	}

	// --- Pipeline: Float32 PCM (16kHz from VAD) → WAV blob → STT → LLM/inject → streaming TTS ---
	async function runPipeline(vadAudio: Float32Array) {
		// 1. Encode VAD audio as WAV (16kHz mono int16) for Speaches
		const wavBlob = float32ToWav(vadAudio, 16000);

		// 2. STT
		let userText = '';
		try {
			const form = new FormData();
			form.append('file', wavBlob, 'recording.wav');
			const resp = await fetch('/api/swarm/transcribe', { method: 'POST', body: form });
			if (!resp.ok) {
				const e = await resp.json().catch(() => ({ error: resp.statusText }));
				throw new Error(e.error ?? resp.statusText);
			}
			userText = ((await resp.json()).text ?? '').trim();
		} catch (err) {
			setError(err instanceof Error ? err.message : 'Transcription failed');
			maybeLoop();
			return;
		}

		if (!userText) {
			pipelineState = 'idle';
			maybeLoop();
			return;
		}

		history = [...history, { role: 'user', text: userText }];
		scrollBottom();

		// 3. LLM or orchestrator inject
		pipelineState = 'thinking';
		let assistantText = '';

		if (mode === 'relay' && sessionId) {
			// Direct relay to orchestrator — inject and report recent output
			try {
				const resp = await fetch('/api/swarm/voice/inject', {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({ message: userText, session_id: sessionId })
				});
				if (!resp.ok) {
					const e = await resp.json().catch(() => ({ error: resp.statusText }));
					throw new Error(e.error ?? resp.statusText);
				}
				const data = await resp.json();
				const recent = (data.recent_output ?? '').trim();
				const agentName = data.agent_name ?? 'orchestrator';
				assistantText = recent
					? `Sent to ${agentName}. They were last saying: ${recent.slice(-200)}`
					: `Sent to ${agentName}.`;
				// Summarise via haiku for TTS-friendly phrasing
				assistantText = await summariseForVoice(userText, assistantText);
			} catch (err) {
				setError(err instanceof Error ? err.message : 'Inject failed');
				maybeLoop();
				return;
			}
		} else {
			// Assistant mode — Claude haiku with SwarmOps context
			try {
				const resp = await fetch('/api/swarm/voice/chat', {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify({
						message: userText,
						session_id: sessionId,
						history: history.slice(0, -1)
							.filter((t) => t.role === 'user')
							.map((t) => ({ role: t.role, content: t.text }))
					})
				});
				if (!resp.ok) {
					const e = await resp.json().catch(() => ({ error: resp.statusText }));
					throw new Error(e.error ?? resp.statusText);
				}
				assistantText = ((await resp.json()).text ?? '').trim();
			} catch (err) {
				setError(err instanceof Error ? err.message : 'Assistant error');
				maybeLoop();
				return;
			}
		}

		if (!assistantText) {
			pipelineState = 'idle';
			maybeLoop();
			return;
		}

		history = [...history, { role: 'assistant', text: assistantText }];
		scrollBottom();

		// 4. Streaming PCM TTS
		pipelineState = 'speaking';
		try {
			await streamTTS(assistantText);
		} catch (err) {
			// TTS failure is non-fatal — text is shown, just no audio
			if ((err as Error).name !== 'AbortError') {
				setError('TTS failed — text shown above');
			}
		}

		pipelineState = 'idle';
		maybeLoop();
	}

	// Summarise orchestrator relay output into 1-2 voice-friendly sentences
	async function summariseForVoice(userMsg: string, rawOutput: string): Promise<string> {
		try {
			const resp = await fetch('/api/swarm/voice/chat', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					message: `The user said: "${userMsg}". The orchestrator's recent output: "${rawOutput.slice(-300)}". Summarise in 1-2 spoken sentences what the orchestrator is doing or just confirmed.`,
					session_id: sessionId,
					history: []
				})
			});
			if (!resp.ok) return rawOutput.slice(0, 200);
			return ((await resp.json()).text ?? rawOutput).trim();
		} catch {
			return rawOutput.slice(0, 200);
		}
	}

	// --- Streaming PCM TTS via Web Audio API ---
	async function streamTTS(text: string) {
		ttsAbort = new AbortController();
		const ctx = ensureAudioCtx();
		if (ctx.state === 'suspended') await ctx.resume();

		const resp = await fetch('/api/swarm/tts', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ text, format: 'pcm' }),
			signal: ttsAbort.signal
		});

		if (!resp.ok || !resp.body) {
			throw new Error(`TTS error ${resp.status}`);
		}

		const reader = resp.body.getReader();
		// Schedule audio chunks back-to-back starting 50ms from now
		let nextStart = ctx.currentTime + 0.05;
		// Accumulate partial bytes across chunk boundaries (PCM is 2 bytes/sample)
		let leftover = new Uint8Array(0);

		while (true) {
			const { done, value } = await reader.read();
			if (done) break;

			// Merge leftover from previous chunk
			const combined = new Uint8Array(leftover.length + value.length);
			combined.set(leftover);
			combined.set(value, leftover.length);

			// Keep even number of bytes (PCM int16 = 2 bytes/sample)
			const usable = combined.length - (combined.length % 2);
			leftover = combined.slice(usable);

			if (usable === 0) continue;

			const view = new DataView(combined.buffer, combined.byteOffset, usable);
			const sampleCount = usable / 2;
			const float32 = new Float32Array(sampleCount);
			for (let i = 0; i < sampleCount; i++) {
				float32[i] = view.getInt16(i * 2, true) / 32768.0;
			}

			const buffer = ctx.createBuffer(1, sampleCount, 24000);
			buffer.getChannelData(0).set(float32);
			const source = ctx.createBufferSource();
			source.buffer = buffer;
			source.connect(ctx.destination);
			source.onended = () => {
				activeSources = activeSources.filter((s) => s !== source);
			};
			activeSources.push(source);
			source.start(nextStart);
			nextStart += buffer.duration;
		}

		// Wait for all audio to finish playing
		if (activeSources.length > 0) {
			await new Promise<void>((resolve) => {
				const last = activeSources[activeSources.length - 1];
				const prevOnEnded = last.onended;
				last.onended = (e) => {
					if (typeof prevOnEnded === 'function') prevOnEnded.call(last, e);
					resolve();
				};
			});
		}
	}

	// --- VAD audio → WAV encoding ---
	function float32ToWav(samples: Float32Array, sampleRate: number): Blob {
		const numSamples = samples.length;
		const buffer = new ArrayBuffer(44 + numSamples * 2);
		const view = new DataView(buffer);

		// WAV header
		const writeStr = (offset: number, s: string) => {
			for (let i = 0; i < s.length; i++) view.setUint8(offset + i, s.charCodeAt(i));
		};
		writeStr(0, 'RIFF');
		view.setUint32(4, 36 + numSamples * 2, true);
		writeStr(8, 'WAVE');
		writeStr(12, 'fmt ');
		view.setUint32(16, 16, true);       // PCM chunk size
		view.setUint16(20, 1, true);        // PCM format
		view.setUint16(22, 1, true);        // mono
		view.setUint32(24, sampleRate, true);
		view.setUint32(28, sampleRate * 2, true); // byte rate
		view.setUint16(32, 2, true);        // block align
		view.setUint16(34, 16, true);       // bits per sample
		writeStr(36, 'data');
		view.setUint32(40, numSamples * 2, true);

		// PCM samples (float32 → int16)
		for (let i = 0; i < numSamples; i++) {
			const s = Math.max(-1, Math.min(1, samples[i]));
			view.setInt16(44 + i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
		}
		return new Blob([buffer], { type: 'audio/wav' });
	}

	// --- Helpers ---
	function maybeLoop() {
		if (autoLoop && pipelineState === 'idle') {
			startListening();
		}
	}

	function setError(msg: string) {
		pipelineState = 'error';
		errorMessage = msg;
		if (errorTimer) clearTimeout(errorTimer);
		errorTimer = setTimeout(() => {
			if (pipelineState === 'error') { pipelineState = 'idle'; errorMessage = ''; }
		}, 4000);
	}

	function scrollBottom() {
		setTimeout(() => historyEl?.scrollTo({ top: historyEl.scrollHeight, behavior: 'smooth' }), 50);
	}

	const stateLabel: Record<PipelineState, string> = {
		idle: '',
		listening: 'Listening…',
		transcribing: 'Transcribing…',
		thinking: 'Thinking…',
		speaking: 'Speaking…',
		error: ''
	};

	const modeLabel: Record<Mode, string> = {
		assistant: 'SwarmOps Assistant',
		relay: 'Relay to Orchestrator'
	};
</script>

<!-- Overlay backdrop -->
<div
	class="fixed inset-0 z-50 flex items-end sm:items-center justify-center p-4 bg-black/40 backdrop-blur-sm"
	role="dialog"
	aria-modal="true"
	aria-label="Voice conversation"
>
	<div class="w-full max-w-md bg-vanna-cream rounded-2xl shadow-2xl flex flex-col overflow-hidden max-h-[85vh]">

		<!-- Title bar -->
		<div class="flex items-center justify-between px-4 py-3 border-b border-slate-200/60">
			<div class="flex items-center gap-2">
				<svg class="w-4 h-4 text-vanna-teal" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
						d="M19 11a7 7 0 01-7 7m0 0a7 7 0 01-7-7m7 7v4m0 0H8m4 0h4m-4-8a3 3 0 01-3-3V5a3 3 0 116 0v6a3 3 0 01-3 3z" />
				</svg>
				<span class="font-semibold text-vanna-navy text-sm">{modeLabel[mode]}</span>
			</div>
			<button
				type="button"
				onclick={onClose}
				class="p-1.5 rounded-lg text-slate-400 hover:text-vanna-navy hover:bg-slate-100 transition-colors"
				aria-label="Close voice assistant"
			>
				<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
				</svg>
			</button>
		</div>

		<!-- Mode toggle -->
		{#if sessionId}
		<div class="flex border-b border-slate-200/60">
			<button
				type="button"
				onclick={() => { mode = 'assistant'; }}
				class="flex-1 py-2 text-xs font-medium transition-colors
					{mode === 'assistant' ? 'bg-vanna-teal text-white' : 'text-slate-500 hover:text-vanna-navy'}"
			>
				Assistant
			</button>
			<button
				type="button"
				onclick={() => { mode = 'relay'; }}
				class="flex-1 py-2 text-xs font-medium transition-colors
					{mode === 'relay' ? 'bg-vanna-navy text-white' : 'text-slate-500 hover:text-vanna-navy'}"
			>
				Relay to Orchestrator
			</button>
		</div>
		{/if}

		<!-- Mode description -->
		<div class="px-4 py-2 text-xs text-slate-400 border-b border-slate-100">
			{#if mode === 'assistant'}
				Ask about session status, agents, tasks, or give instructions.
			{:else}
				Your voice goes directly into the orchestrator's Claude Code session.
			{/if}
		</div>

		<!-- Conversation history -->
		<div bind:this={historyEl} class="flex-1 overflow-y-auto px-4 py-3 space-y-3 min-h-0">
			{#if history.length === 0}
				<p class="text-center text-slate-400 text-sm py-8">
					{vadReady ? 'Tap the mic to start' : 'Loading mic…'}
				</p>
			{/if}
			{#each history as turn}
				<div class="flex {turn.role === 'user' ? 'justify-end' : 'justify-start'}">
					<div
						class="max-w-[82%] px-3 py-2 rounded-2xl text-sm leading-relaxed
							{turn.role === 'user'
								? 'bg-vanna-teal text-white rounded-br-sm'
								: mode === 'relay'
									? 'bg-vanna-navy/10 border border-vanna-navy/20 text-vanna-navy rounded-bl-sm'
									: 'bg-white border border-slate-200 text-vanna-navy rounded-bl-sm'}"
					>
						{turn.text}
					</div>
				</div>
			{/each}
		</div>

		<!-- Controls -->
		<div class="px-5 py-4 border-t border-slate-200/60">
			<!-- Status line -->
			<div class="text-center mb-3 h-4">
				{#if pipelineState === 'error' && errorMessage}
					<span class="text-xs text-vanna-orange">{errorMessage}</span>
				{:else if pipelineState !== 'idle'}
					<span class="text-xs text-vanna-teal font-medium animate-pulse">{stateLabel[pipelineState]}</span>
				{/if}
			</div>

			<div class="flex items-center justify-center gap-4">
				<!-- Auto-loop toggle -->
				<button
					type="button"
					onclick={() => (autoLoop = !autoLoop)}
					title={autoLoop ? 'Auto-loop on' : 'Auto-loop off'}
					disabled={!vadReady}
					class="w-9 h-9 rounded-full flex items-center justify-center transition-colors border disabled:opacity-30
						{autoLoop
							? 'bg-vanna-teal/10 border-vanna-teal text-vanna-teal'
							: 'bg-white border-slate-200 text-slate-400 hover:border-vanna-teal/40 hover:text-vanna-teal'}"
				>
					<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
							d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
					</svg>
				</button>

				<!-- Main mic button -->
				<button
					type="button"
					onclick={handleMainButton}
					disabled={!vadReady || pipelineState === 'transcribing' || pipelineState === 'thinking' || pipelineState === 'speaking'}
					title={pipelineState === 'listening' ? 'Stop' : 'Start talking'}
					class="relative w-16 h-16 rounded-full flex items-center justify-center transition-all focus:outline-none focus:ring-2 focus:ring-offset-2 disabled:opacity-40 disabled:cursor-not-allowed shadow-md
						{pipelineState === 'listening'
							? 'bg-red-500 hover:bg-red-600 focus:ring-red-400 text-white'
							: pipelineState === 'speaking'
								? 'bg-vanna-teal text-white focus:ring-vanna-teal'
								: pipelineState === 'error'
									? 'bg-vanna-orange text-white focus:ring-vanna-orange'
									: mode === 'relay'
										? 'bg-vanna-navy text-white hover:bg-vanna-navy/90 focus:ring-vanna-navy'
										: 'bg-vanna-teal text-white hover:bg-vanna-teal/90 focus:ring-vanna-teal'}"
				>
					{#if pipelineState === 'listening'}
						<span class="absolute inset-0 rounded-full bg-red-400 animate-ping opacity-25"></span>
						<!-- Stop square -->
						<svg class="w-6 h-6 relative z-10" fill="currentColor" viewBox="0 0 24 24">
							<rect x="6" y="6" width="12" height="12" rx="1.5" />
						</svg>
					{:else if pipelineState === 'transcribing' || pipelineState === 'thinking'}
						<svg class="w-6 h-6 animate-spin" fill="none" viewBox="0 0 24 24">
							<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
							<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
						</svg>
					{:else if pipelineState === 'speaking'}
						<!-- Speaker wave -->
						<svg class="w-6 h-6" fill="currentColor" viewBox="0 0 24 24">
							<path d="M3 9v6h4l5 5V4L7 9H3zm13.5 3c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02z"/>
						</svg>
					{:else if pipelineState === 'error'}
						<svg class="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
								d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
						</svg>
					{:else}
						<!-- Mic -->
						<svg class="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
								d="M19 11a7 7 0 01-7 7m0 0a7 7 0 01-7-7m7 7v4m0 0H8m4 0h4m-4-8a3 3 0 01-3-3V5a3 3 0 116 0v6a3 3 0 01-3 3z" />
						</svg>
					{/if}
				</button>

				<!-- Clear history -->
				<button
					type="button"
					onclick={() => (history = [])}
					title="Clear conversation"
					disabled={history.length === 0}
					class="w-9 h-9 rounded-full flex items-center justify-center transition-colors border border-slate-200 text-slate-400 hover:border-red-200 hover:text-red-400 bg-white disabled:opacity-30 disabled:cursor-not-allowed"
					aria-label="Clear conversation history"
				>
					<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
							d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
					</svg>
				</button>
			</div>
		</div>
	</div>
</div>
