<script lang="ts">
	import { onMount, onDestroy } from 'svelte';

	interface Props {
		onTranscript: (text: string) => void;
		disabled?: boolean;
	}

	let { onTranscript, disabled = false }: Props = $props();

	type RecognitionState = 'idle' | 'listening' | 'processing' | 'error';

	let state = $state<RecognitionState>('idle');
	let interim = $state('');
	let errorMessage = $state('');
	let supported = $state(false);

	let recognition: SpeechRecognition | null = null;

	onMount(() => {
		const SR = (window as any).SpeechRecognition ?? (window as any).webkitSpeechRecognition;
		supported = !!SR;
		if (!SR) return;

		recognition = new SR() as SpeechRecognition;
		recognition.continuous = false;
		recognition.interimResults = true;
		recognition.lang = 'en-GB';
		recognition.maxAlternatives = 1;

		recognition.onresult = (event: SpeechRecognitionEvent) => {
			let interimTranscript = '';
			let finalTranscript = '';

			for (let i = event.resultIndex; i < event.results.length; i++) {
				const result = event.results[i];
				if (result.isFinal) {
					finalTranscript += result[0].transcript;
				} else {
					interimTranscript += result[0].transcript;
				}
			}

			interim = interimTranscript;

			if (finalTranscript.trim()) {
				interim = '';
				state = 'idle';
				onTranscript(finalTranscript.trim());
			}
		};

		recognition.onerror = (event: SpeechRecognitionErrorEvent) => {
			// 'no-speech' and 'aborted' are expected — not real errors
			if (event.error === 'no-speech' || event.error === 'aborted') {
				state = 'idle';
				interim = '';
				return;
			}
			errorMessage = event.error === 'not-allowed'
				? 'Microphone access denied'
				: `Voice error: ${event.error}`;
			state = 'error';
			interim = '';
			// Clear error after 3s
			setTimeout(() => {
				if (state === 'error') {
					state = 'idle';
					errorMessage = '';
				}
			}, 3000);
		};

		recognition.onend = () => {
			// If we were still listening and it stopped (e.g. iOS auto-stop), go back to idle
			if (state === 'listening') {
				state = 'idle';
				interim = '';
			}
		};
	});

	onDestroy(() => {
		if (recognition && state === 'listening') {
			recognition.abort();
		}
	});

	function toggle() {
		if (!recognition || disabled) return;

		if (state === 'listening') {
			recognition.stop();
			state = 'idle';
			interim = '';
		} else {
			errorMessage = '';
			state = 'listening';
			interim = '';
			try {
				recognition.start();
			} catch {
				// Recognition already started (can happen on rapid taps)
				state = 'idle';
			}
		}
	}
</script>

{#if supported}
	<div class="flex items-center gap-2">
		<!-- Mic button -->
		<button
			type="button"
			onclick={toggle}
			disabled={disabled || state === 'error'}
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
					<rect x="6" y="6" width="12" height="12" rx="1"/>
				</svg>
			{:else if state === 'error'}
				<!-- Warning icon -->
				<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/>
				</svg>
			{:else}
				<!-- Mic icon -->
				<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 11a7 7 0 01-7 7m0 0a7 7 0 01-7-7m7 7v4m0 0H8m4 0h4m-4-8a3 3 0 01-3-3V5a3 3 0 116 0v6a3 3 0 01-3 3z"/>
				</svg>
			{/if}
		</button>

		<!-- Interim transcript preview -->
		{#if interim}
			<span class="text-xs text-slate-400 italic truncate max-w-48 sm:max-w-xs">
				{interim}…
			</span>
		{:else if state === 'listening'}
			<span class="text-xs text-red-500 font-medium animate-pulse">Listening…</span>
		{:else if errorMessage}
			<span class="text-xs text-vanna-orange">{errorMessage}</span>
		{/if}
	</div>
{/if}
