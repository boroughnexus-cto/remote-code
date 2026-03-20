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
		agents: Agent[];
		onMessage: (text: string, agentId?: string) => Promise<void>;
	}

	let { sessionId, agents, onMessage }: Props = $props();

	let inputText = $state('');
	let targetAgentId = $state('');  // '' = broadcast to all
	let sending = $state(false);
	let lastError = $state('');
	let autoSend = $state(false);

	const liveAgents = $derived(agents.filter((a) => !!a.tmux_session));

	async function send() {
		const text = inputText.trim();
		if (!text || sending) return;
		sending = true;
		lastError = '';
		try {
			await onMessage(text, targetAgentId || undefined);
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
			inputText = text;
			send();
		} else {
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
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 12h.01M12 12h.01M16 12h.01M21 12c0 4.418-4.03 8-9 8a9.863 9.863 0 01-4.255-.949L3 20l1.395-3.72C3.512 15.042 3 13.574 3 12c0-4.418 4.03-8 9-8s9 3.582 9 8z" />
				</svg>
			</div>
			<div>
				<p class="font-semibold text-sm text-vanna-navy">Message Workers</p>
				<p class="text-xs text-slate-400">
					{liveAgents.length} live agent{liveAgents.length !== 1 ? 's' : ''}
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
		</div>
	</div>

	<!-- Target selector (shown when >1 live agent) -->
	{#if liveAgents.length > 1}
		<div class="mb-3">
			<select
				bind:value={targetAgentId}
				class="w-full text-xs px-3 py-1.5 border border-slate-200 rounded-lg focus:outline-none focus:ring-2 focus:ring-vanna-magenta/40 text-slate-600 bg-white"
			>
				<option value="">All live agents (broadcast)</option>
				{#each liveAgents as agent}
					<option value={agent.id}>{agent.name} ({agent.role})</option>
				{/each}
			</select>
		</div>
	{/if}

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
				placeholder={targetAgentId
					? `Message ${liveAgents.find((a) => a.id === targetAgentId)?.name ?? 'agent'}… (Enter to send)`
					: 'Broadcast to all workers… (Enter to send, Shift+Enter for newline)'}
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
		{autoSend ? '⚡ Auto-send active — voice goes straight to workers.' : 'Mic fills the box for review. Toggle ⚡ Auto to send instantly.'}
	</p>
</div>
