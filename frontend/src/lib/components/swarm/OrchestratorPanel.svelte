<script lang="ts">
	import VoiceInput from '$lib/components/VoiceInput.svelte';

	interface Agent {
		id: string;
		name: string;
		role: string;
		status: string;
		tmux_session?: string | null;
	}

	interface Props {
		sessionId: string;
		orchestrator: Agent;
		onMessage: (text: string) => Promise<void>;
	}

	let { sessionId, orchestrator, onMessage }: Props = $props();

	let inputText = $state('');
	let sending = $state(false);
	let lastError = $state('');
	let autoSend = $state(false);

	const terminalHref = $derived(
		orchestrator.tmux_session ? `/terminal/${orchestrator.tmux_session}` : null
	);

	const statusColors: Record<string, string> = {
		idle: 'text-slate-400',
		thinking: 'text-blue-500',
		coding: 'text-vanna-teal',
		waiting: 'text-orange-500',
		stuck: 'text-red-500',
		done: 'text-green-500'
	};

	async function send() {
		const text = inputText.trim();
		if (!text || sending) return;
		sending = true;
		lastError = '';
		try {
			await onMessage(text);
			inputText = '';
		} catch (e: any) {
			lastError = e?.message ?? 'Failed to send';
		} finally {
			sending = false;
		}
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter' && !e.shiftKey) {
			e.preventDefault();
			send();
		}
	}

	function handleTranscript(text: string) {
		if (autoSend) {
			// Auto-send: fire directly without review
			inputText = text;
			send();
		} else {
			// Manual: fill textarea so user can review/edit before sending
			inputText = text;
		}
	}
</script>

<div class="bg-white/90 rounded-2xl border border-vanna-magenta/30 shadow-vanna-card p-5">
	<!-- Header -->
	<div class="flex items-center justify-between mb-4">
		<div class="flex items-center gap-3">
			<div class="w-9 h-9 rounded-xl bg-vanna-magenta/20 flex items-center justify-center">
				<svg class="w-5 h-5 text-vanna-magenta" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9.663 17h4.673M12 3v1m6.364 1.636l-.707.707M21 12h-1M4 12H3m3.343-5.657l-.707-.707m2.828 9.9a5 5 0 117.072 0l-.548.547A3.374 3.374 0 0014 18.469V19a2 2 0 11-4 0v-.531c0-.895-.356-1.754-.988-2.386l-.548-.547z" />
				</svg>
			</div>
			<div>
				<p class="font-semibold text-sm text-vanna-navy">{orchestrator.name}</p>
				<p class="text-xs {statusColors[orchestrator.status] ?? 'text-slate-400'}">
					{orchestrator.status} · Orchestrator
				</p>
			</div>
		</div>
		<div class="flex items-center gap-2">
			<!-- Auto-send toggle -->
			<button
				type="button"
				onclick={() => (autoSend = !autoSend)}
				title={autoSend ? 'Auto-send on (voice sends immediately)' : 'Auto-send off (voice fills textarea)'}
				class="flex items-center gap-1 text-xs px-2 py-1 rounded-lg border transition-colors
					{autoSend
						? 'border-vanna-magenta/50 bg-vanna-magenta/10 text-vanna-magenta font-medium'
						: 'border-slate-200 text-slate-400 hover:border-vanna-magenta/30'}"
			>
				<svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/>
				</svg>
				Auto
			</button>
			<span class="w-2 h-2 rounded-full bg-vanna-magenta animate-pulse"></span>
			<span class="text-xs text-vanna-magenta font-medium">Live</span>
			{#if terminalHref}
				<a
					href={terminalHref}
					class="flex items-center gap-1 text-xs px-2.5 py-1.5 rounded-lg bg-vanna-magenta/10 text-vanna-magenta hover:bg-vanna-magenta/20 transition-colors font-medium"
				>
					<svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"/>
					</svg>
					View terminal
				</a>
			{/if}
		</div>
	</div>

	<!-- Input area -->
	<div class="flex gap-2 items-end">
		<!-- Voice input -->
		<div class="flex-shrink-0 self-center">
			<VoiceInput onTranscript={handleTranscript} disabled={sending} />
		</div>

		<div class="flex-1">
			<textarea
				bind:value={inputText}
				onkeydown={handleKeydown}
				placeholder="Tell the orchestrator what you need… (Enter to send, Shift+Enter for newline)"
				rows={2}
				class="w-full px-3 py-2.5 text-sm border border-slate-200 rounded-xl resize-none focus:outline-none focus:ring-2 focus:ring-vanna-magenta/40 focus:border-vanna-magenta transition-all placeholder:text-slate-300"
				disabled={sending}
			></textarea>
		</div>

		<button
			type="button"
			onclick={send}
			disabled={sending || !inputText.trim()}
			class="flex items-center gap-1.5 px-4 py-2.5 rounded-xl bg-vanna-magenta text-white text-sm font-medium hover:bg-vanna-magenta/90 disabled:opacity-50 transition-colors flex-shrink-0"
		>
			{#if sending}
				<svg class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24">
					<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4"></circle>
					<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"></path>
				</svg>
			{:else}
				<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 19l9 2-9-18-9 18 9-2zm0 0v-8" />
				</svg>
			{/if}
			Send
		</button>
	</div>

	{#if lastError}
		<p class="text-xs text-red-500 mt-2">{lastError}</p>
	{/if}

	<p class="text-xs text-slate-300 mt-2">
		{autoSend ? '⚡ Auto-send active — voice goes straight to the orchestrator.' : 'Mic fills the box for review. Toggle ⚡ Auto to send instantly.'}
	</p>
</div>
