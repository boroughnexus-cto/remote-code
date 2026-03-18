<script lang="ts">
	import { onMount, onDestroy } from 'svelte';

	interface Props {
		onTranscript: (text: string) => void;
		disabled?: boolean;
	}

	let { onTranscript, disabled = false }: Props = $props();

	type RecordingState = 'idle' | 'listening' | 'processing' | 'error';

	let state = $state<RecordingState>('idle');
	let errorMessage = $state('');
	let supported = $state(false);

	let mediaRecorder: MediaRecorder | null = null;
	let audioChunks: Blob[] = [];
	let stream: MediaStream | null = null;
	let mimeType = '';
	let errorTimers: ReturnType<typeof setTimeout>[] = [];

	onMount(() => {
		supported = !!(
			typeof navigator !== 'undefined' &&
			navigator.mediaDevices &&
			typeof navigator.mediaDevices.getUserMedia === 'function' &&
			typeof MediaRecorder !== 'undefined'
		);

		// Pick the best MIME type: prefer webm/opus (Chrome), fall back to mp4 (Safari)
		if (supported) {
			if (MediaRecorder.isTypeSupported('audio/webm;codecs=opus')) {
				mimeType = 'audio/webm;codecs=opus';
			} else if (MediaRecorder.isTypeSupported('audio/mp4')) {
				mimeType = 'audio/mp4';
			}
		}
	});

	onDestroy(() => {
		stopStream();
		errorTimers.forEach((t) => clearTimeout(t));
		errorTimers = [];
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

	async function toggle() {
		if (disabled) return;

		if (state === 'listening') {
			// Stop recording — onstop will handle transcription
			if (mediaRecorder && mediaRecorder.state !== 'inactive') {
				mediaRecorder.stop();
			}
			return;
		}

		// Start recording
		errorMessage = '';
		try {
			stream = await navigator.mediaDevices.getUserMedia({ audio: true });
			audioChunks = [];

			const options = mimeType ? { mimeType } : {};
			mediaRecorder = new MediaRecorder(stream, options);

			mediaRecorder.ondataavailable = (e) => {
				if (e.data.size > 0) audioChunks.push(e.data);
			};

			mediaRecorder.onstop = async () => {
				stream?.getTracks().forEach((t) => t.stop());
				stream = null;

				if (audioChunks.length === 0) {
					state = 'idle';
					return;
				}

				state = 'processing';
				// Use the actual MIME type the recorder chose (may differ from our initial preference)
				const actualMime = mediaRecorder?.mimeType || mimeType || 'audio/webm';
				const ext = actualMime.includes('mp4') ? 'm4a' : 'webm';
				const blob = new Blob(audioChunks, { type: actualMime });

				try {
					const form = new FormData();
					form.append('file', blob, `recording.${ext}`);

					const resp = await fetch('/api/swarm/transcribe', {
						method: 'POST',
						body: form
					});

					if (!resp.ok) {
						const err = await resp.json().catch(() => ({ error: resp.statusText }));
						throw new Error(err.error ?? resp.statusText);
					}

					const data = await resp.json();
					const text = (data.text ?? '').trim();
					if (text) onTranscript(text);
					state = 'idle';
				} catch (err: unknown) {
					const msg = err instanceof Error ? err.message : String(err);
					errorMessage = msg.length > 60 ? 'Transcription failed' : msg;
					state = 'error';
					errorTimers.push(
						setTimeout(() => {
							if (state === 'error') {
								state = 'idle';
								errorMessage = '';
							}
						}, 3000)
					);
				}
			};

			mediaRecorder.start();
			state = 'listening';
		} catch (err: unknown) {
			const e = err as DOMException;
			errorMessage =
				e.name === 'NotAllowedError' ? 'Microphone access denied' : `Mic error: ${e.message}`;
			state = 'error';
			errorTimers.push(
				setTimeout(() => {
					if (state === 'error') {
						state = 'idle';
						errorMessage = '';
					}
				}, 3000)
			);
		}
	}
</script>

{#if supported}
	<div class="flex items-center gap-2">
		<!-- Mic button -->
		<button
			type="button"
			onclick={toggle}
			disabled={disabled || state === 'processing'}
			title={state === 'listening' ? 'Stop recording' : 'Start voice input'}
			class="relative flex-shrink-0 w-10 h-10 rounded-full flex items-center justify-center transition-all focus:outline-none focus:ring-2 focus:ring-offset-2 disabled:opacity-40 disabled:cursor-not-allowed
				{state === 'listening'
				? 'bg-red-500 hover:bg-red-600 focus:ring-red-500 text-white shadow-lg'
				: state === 'error'
					? 'bg-vanna-orange text-white focus:ring-vanna-orange'
					: 'bg-vanna-cream hover:bg-vanna-teal/10 border border-vanna-teal/40 text-vanna-teal focus:ring-vanna-teal'}"
		>
			{#if state === 'listening'}
				<!-- Pulse ring -->
				<span class="absolute inset-0 rounded-full bg-red-400 animate-ping opacity-30"></span>
				<!-- Stop icon -->
				<svg class="w-4 h-4 relative z-10" fill="currentColor" viewBox="0 0 24 24">
					<rect x="6" y="6" width="12" height="12" rx="1" />
				</svg>
			{:else if state === 'processing'}
				<!-- Spinner -->
				<svg class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24">
					<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"
					></circle>
					<path
						class="opacity-75"
						fill="currentColor"
						d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
					></path>
				</svg>
			{:else if state === 'error'}
				<!-- Warning icon -->
				<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path
						stroke-linecap="round"
						stroke-linejoin="round"
						stroke-width="2"
						d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
					/>
				</svg>
			{:else}
				<!-- Mic icon -->
				<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path
						stroke-linecap="round"
						stroke-linejoin="round"
						stroke-width="2"
						d="M19 11a7 7 0 01-7 7m0 0a7 7 0 01-7-7m7 7v4m0 0H8m4 0h4m-4-8a3 3 0 01-3-3V5a3 3 0 116 0v6a3 3 0 01-3 3z"
					/>
				</svg>
			{/if}
		</button>

		<!-- Status text -->
		{#if state === 'listening'}
			<span class="text-xs text-red-500 font-medium animate-pulse">Listening…</span>
		{:else if state === 'processing'}
			<span class="text-xs text-vanna-teal font-medium animate-pulse">Transcribing…</span>
		{:else if errorMessage}
			<span class="text-xs text-vanna-orange">{errorMessage}</span>
		{/if}
	</div>
{/if}
