<script lang="ts">
	import { onDestroy } from 'svelte';

	interface Props {
		sessionId: string;
		onClose: () => void;
	}

	let { sessionId, onClose }: Props = $props();

	type ConvState = 'idle' | 'listening' | 'transcribing' | 'thinking' | 'speaking' | 'error';

	interface Turn {
		role: 'user' | 'assistant';
		text: string;
	}

	let state = $state<ConvState>('idle');
	let errorMessage = $state('');
	let history = $state<Turn[]>([]);
	let autoLoop = $state(false);
	let supported = $state(false);
	let mimeType = $state('');

	// MediaStream acquired once on first user gesture and reused across turns
	let stream: MediaStream | null = null;
	// AudioContext unlocked once on first user gesture for iOS autoplay
	let audioCtx: AudioContext | null = null;
	let mediaRecorder: MediaRecorder | null = null;
	let audioChunks: Blob[] = [];
	let errorTimer: ReturnType<typeof setTimeout> | null = null;

	// Check browser support
	supported = !!(
		typeof navigator !== 'undefined' &&
		navigator.mediaDevices &&
		typeof navigator.mediaDevices.getUserMedia === 'function' &&
		typeof MediaRecorder !== 'undefined'
	);
	if (supported) {
		if (MediaRecorder.isTypeSupported('audio/webm;codecs=opus')) {
			mimeType = 'audio/webm;codecs=opus';
		} else if (MediaRecorder.isTypeSupported('audio/mp4')) {
			mimeType = 'audio/mp4';
		}
	}

	onDestroy(() => {
		stopStream();
		if (audioCtx) {
			audioCtx.close();
			audioCtx = null;
		}
		if (errorTimer) clearTimeout(errorTimer);
	});

	function stopStream() {
		if (mediaRecorder && mediaRecorder.state !== 'inactive') {
			mediaRecorder.stop();
		}
		if (stream) {
			stream.getTracks().forEach((t) => t.stop());
			stream = null;
		}
	}

	function setError(msg: string) {
		errorMessage = msg;
		state = 'error';
		if (errorTimer) clearTimeout(errorTimer);
		errorTimer = setTimeout(() => {
			if (state === 'error') {
				state = 'idle';
				errorMessage = '';
			}
		}, 4000);
	}

	/** Unlock iOS AudioContext and acquire mic stream — must be called from a real user gesture */
	async function ensureStreamAndAudio() {
		// Unlock AudioContext for iOS
		if (!audioCtx) {
			audioCtx = new (window.AudioContext || (window as unknown as { webkitAudioContext: typeof AudioContext }).webkitAudioContext)();
		}
		if (audioCtx.state === 'suspended') {
			await audioCtx.resume();
		}

		// Acquire mic stream once
		if (!stream || !stream.active) {
			stream = await navigator.mediaDevices.getUserMedia({ audio: true });
		}
	}

	async function startListening() {
		try {
			await ensureStreamAndAudio();
		} catch (err: unknown) {
			const e = err as DOMException;
			setError(e.name === 'NotAllowedError' ? 'Microphone access denied' : 'Mic error');
			return;
		}

		audioChunks = [];
		const options = mimeType ? { mimeType } : {};
		mediaRecorder = new MediaRecorder(stream!, options);

		mediaRecorder.ondataavailable = (e) => {
			if (e.data.size > 0) audioChunks.push(e.data);
		};

		mediaRecorder.onstop = async () => {
			await runPipeline();
		};

		mediaRecorder.start();
		state = 'listening';
	}

	function stopListening() {
		if (mediaRecorder && mediaRecorder.state !== 'inactive') {
			mediaRecorder.stop();
			// state transitions in onstop → runPipeline
		}
	}

	/** Full pipeline: transcribe → chat → speak */
	async function runPipeline() {
		if (audioChunks.length === 0) {
			state = 'idle';
			return;
		}

		// 1. Transcribe
		state = 'transcribing';
		const actualMime = mediaRecorder?.mimeType || mimeType || 'audio/webm';
		const ext = actualMime.includes('mp4') ? 'm4a' : 'webm';
		const blob = new Blob(audioChunks, { type: actualMime });

		let userText = '';
		try {
			const form = new FormData();
			form.append('file', blob, `recording.${ext}`);
			const resp = await fetch('/api/swarm/transcribe', { method: 'POST', body: form });
			if (!resp.ok) {
				const err = await resp.json().catch(() => ({ error: resp.statusText }));
				throw new Error(err.error ?? resp.statusText);
			}
			const data = await resp.json();
			userText = (data.text ?? '').trim();
		} catch (err: unknown) {
			setError(err instanceof Error ? err.message : 'Transcription failed');
			return;
		}

		if (!userText) {
			state = 'idle';
			if (autoLoop) startListening();
			return;
		}

		history = [...history, { role: 'user', text: userText }];

		// 2. Chat (Claude haiku with SwarmOps context)
		state = 'thinking';
		let assistantText = '';
		try {
			const resp = await fetch('/api/swarm/voice/chat', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					message: userText,
					session_id: sessionId,
					// Only send user turns — server enforces this but we pre-filter too
					history: history.slice(0, -1).filter((t) => t.role === 'user').map((t) => ({
						role: t.role,
						content: t.text
					}))
				})
			});
			if (!resp.ok) {
				const err = await resp.json().catch(() => ({ error: resp.statusText }));
				throw new Error(err.error ?? resp.statusText);
			}
			const data = await resp.json();
			assistantText = (data.text ?? '').trim();
		} catch (err: unknown) {
			setError(err instanceof Error ? err.message : 'Assistant error');
			return;
		}

		if (!assistantText) {
			state = 'idle';
			if (autoLoop) startListening();
			return;
		}

		history = [...history, { role: 'assistant', text: assistantText }];

		// 3. Speak (Kokoro TTS via AudioContext for iOS)
		state = 'speaking';
		try {
			const resp = await fetch('/api/swarm/tts', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ text: assistantText })
			});
			if (!resp.ok) {
				const err = await resp.json().catch(() => ({ error: resp.statusText }));
				throw new Error(err.error ?? resp.statusText);
			}
			const audioData = await resp.arrayBuffer();
			const decoded = await audioCtx!.decodeAudioData(audioData);
			const source = audioCtx!.createBufferSource();
			source.buffer = decoded;
			source.connect(audioCtx!.destination);
			source.onended = () => {
				state = 'idle';
				if (autoLoop) startListening();
			};
			source.start();
		} catch (err: unknown) {
			// TTS failure is non-fatal — show text, keep going
			setError(err instanceof Error ? err.message : 'TTS failed');
			// Still loop if enabled
			setTimeout(() => {
				if (autoLoop) startListening();
			}, 4000);
		}
	}

	function handleMicButton() {
		if (state === 'listening') {
			autoLoop = false; // Stop loop when user manually stops
			stopListening();
		} else if (state === 'idle' || state === 'error') {
			startListening();
		}
		// ignore clicks during transcribing/thinking/speaking
	}

	const stateLabel: Record<ConvState, string> = {
		idle: 'Tap to speak',
		listening: 'Listening…',
		transcribing: 'Transcribing…',
		thinking: 'Thinking…',
		speaking: 'Speaking…',
		error: ''
	};
</script>

<!-- Overlay backdrop -->
<div
	class="fixed inset-0 z-50 flex items-end sm:items-center justify-center p-4 bg-black/40 backdrop-blur-sm"
	role="dialog"
	aria-modal="true"
	aria-label="Voice conversation"
>
	<!-- Panel -->
	<div class="w-full max-w-md bg-vanna-cream rounded-2xl shadow-2xl flex flex-col overflow-hidden max-h-[80vh]">
		<!-- Title bar -->
		<div class="flex items-center justify-between px-5 py-4 border-b border-slate-200/60">
			<div class="flex items-center gap-2">
				<svg class="w-4 h-4 text-vanna-teal" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
						d="M19 11a7 7 0 01-7 7m0 0a7 7 0 01-7-7m7 7v4m0 0H8m4 0h4m-4-8a3 3 0 01-3-3V5a3 3 0 116 0v6a3 3 0 01-3 3z" />
				</svg>
				<span class="font-semibold text-vanna-navy text-sm">Voice Assistant</span>
			</div>
			<button
				type="button"
				onclick={onClose}
				class="p-1.5 rounded-lg text-slate-400 hover:text-vanna-navy hover:bg-slate-100 transition-colors"
				aria-label="Close"
			>
				<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
				</svg>
			</button>
		</div>

		<!-- Conversation history -->
		<div class="flex-1 overflow-y-auto px-4 py-3 space-y-3 min-h-0">
			{#if history.length === 0}
				<p class="text-center text-slate-400 text-sm py-8">Tap the mic to start talking</p>
			{/if}
			{#each history as turn}
				<div class="flex {turn.role === 'user' ? 'justify-end' : 'justify-start'}">
					<div
						class="max-w-[80%] px-3 py-2 rounded-2xl text-sm leading-relaxed
							{turn.role === 'user'
								? 'bg-vanna-teal text-white rounded-br-sm'
								: 'bg-white border border-slate-200 text-vanna-navy rounded-bl-sm'}"
					>
						{turn.text}
					</div>
				</div>
			{/each}
		</div>

		<!-- Controls -->
		<div class="px-5 py-4 border-t border-slate-200/60">
			{#if !supported}
				<p class="text-center text-slate-400 text-sm">Voice not supported in this browser</p>
			{:else}
				<!-- Status -->
				<div class="text-center mb-3 h-4">
					{#if state === 'error' && errorMessage}
						<span class="text-xs text-vanna-orange">{errorMessage}</span>
					{:else if state !== 'idle'}
						<span class="text-xs text-vanna-teal font-medium animate-pulse">{stateLabel[state]}</span>
					{/if}
				</div>

				<!-- Mic button + auto-loop -->
				<div class="flex items-center justify-center gap-4">
					<!-- Auto-loop toggle -->
					<button
						type="button"
						onclick={() => (autoLoop = !autoLoop)}
						title={autoLoop ? 'Auto-loop on (tap to disable)' : 'Auto-loop off'}
						class="w-9 h-9 rounded-full flex items-center justify-center transition-colors border
							{autoLoop
								? 'bg-vanna-teal/10 border-vanna-teal text-vanna-teal'
								: 'bg-white border-slate-200 text-slate-400 hover:border-vanna-teal/40 hover:text-vanna-teal'}"
					>
						<!-- Loop icon -->
						<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
								d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
						</svg>
					</button>

					<!-- Main mic button -->
					<button
						type="button"
						onclick={handleMicButton}
						disabled={state === 'transcribing' || state === 'thinking' || state === 'speaking'}
						title={state === 'listening' ? 'Stop recording' : 'Start recording'}
						class="relative w-16 h-16 rounded-full flex items-center justify-center transition-all focus:outline-none focus:ring-2 focus:ring-offset-2 disabled:opacity-40 disabled:cursor-not-allowed
							{state === 'listening'
								? 'bg-red-500 hover:bg-red-600 focus:ring-red-500 text-white shadow-lg'
								: state === 'speaking'
									? 'bg-vanna-teal text-white'
									: state === 'error'
										? 'bg-vanna-orange text-white focus:ring-vanna-orange'
										: 'bg-vanna-teal text-white hover:bg-vanna-teal/90 focus:ring-vanna-teal shadow-md'}"
					>
						{#if state === 'listening'}
							<!-- Pulse ring -->
							<span class="absolute inset-0 rounded-full bg-red-400 animate-ping opacity-30"></span>
							<!-- Stop icon -->
							<svg class="w-6 h-6 relative z-10" fill="currentColor" viewBox="0 0 24 24">
								<rect x="6" y="6" width="12" height="12" rx="1" />
							</svg>
						{:else if state === 'transcribing' || state === 'thinking'}
							<!-- Spinner -->
							<svg class="w-6 h-6 animate-spin" fill="none" viewBox="0 0 24 24">
								<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
								<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
							</svg>
						{:else if state === 'speaking'}
							<!-- Sound waves -->
							<svg class="w-6 h-6" fill="currentColor" viewBox="0 0 24 24">
								<path d="M3 9v6h4l5 5V4L7 9H3zm13.5 3c0-1.77-1.02-3.29-2.5-4.03v8.05c1.48-.73 2.5-2.25 2.5-4.02z"/>
							</svg>
						{:else if state === 'error'}
							<!-- Warning -->
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
					>
						<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
								d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16" />
						</svg>
					</button>
				</div>
			{/if}
		</div>
	</div>
</div>
