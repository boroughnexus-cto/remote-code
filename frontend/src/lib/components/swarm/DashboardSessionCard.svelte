<script lang="ts">
	import { goto } from '$app/navigation';
	import KanbanMiniBar from './KanbanMiniBar.svelte';

	interface SessionStats {
		id: string;
		name: string;
		created_at: number;
		updated_at: number;
		agent_count: number;
		live_agents: number;
		stuck_agents: number;
		waiting_agents: number;
		tasks_by_stage: Record<string, number>;
		last_event_ts: number;
		context_name?: string | null;
	}

	interface Props {
		session: SessionStats;
		onDelete: (id: string) => void;
	}

	let { session, onDelete }: Props = $props();

	let totalTasks = $derived(
		Object.values(session.tasks_by_stage).reduce((s, v) => s + v, 0)
	);

	let hasActivity = $derived(session.live_agents > 0);

	function relativeTime(ts: number): string {
		if (!ts) return 'No activity';
		const diff = Math.floor(Date.now() / 1000) - ts;
		if (diff < 60) return 'just now';
		if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
		if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
		return `${Math.floor(diff / 86400)}d ago`;
	}
</script>

<div
	role="link"
	tabindex="0"
	class="bg-white/80 rounded-2xl border border-slate-200 shadow-vanna-card p-5 group hover:shadow-md transition-all duration-200 cursor-pointer
	{session.stuck_agents > 0 ? 'border-red-200' : hasActivity ? 'border-vanna-teal/30' : ''}"
	onclick={(e) => { if (!(e.target as HTMLElement).closest('button')) goto(`/swarm/${session.id}`); }}
	onkeydown={(e) => { if (e.key === 'Enter') goto(`/swarm/${session.id}`); }}
>

	<div class="flex items-start justify-between gap-3 mb-4">
		<a href="/swarm/{session.id}" class="flex items-center gap-3 flex-1 min-w-0">
			<div class="w-10 h-10 rounded-xl flex items-center justify-center flex-shrink-0
				{session.stuck_agents > 0 ? 'bg-red-100' : hasActivity ? 'bg-vanna-teal/10' : 'bg-slate-100'}">
				{#if hasActivity}
					<span class="w-3 h-3 rounded-full bg-vanna-teal animate-pulse"></span>
				{:else}
					<svg class="w-5 h-5 text-slate-400" fill="none" stroke="currentColor" viewBox="0 0 24 24">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
							d="M9 17V7m0 10a2 2 0 01-2 2H5a2 2 0 01-2-2V7a2 2 0 012-2h2a2 2 0 012 2m0 10a2 2 0 002 2h2a2 2 0 002-2M9 7a2 2 0 012-2h2a2 2 0 012 2m0 10V7"
						/>
					</svg>
				{/if}
			</div>
			<div class="min-w-0">
				<p class="font-semibold text-vanna-navy group-hover:text-vanna-teal transition-colors truncate">
					{session.name}
				</p>
				<div class="flex items-center gap-2 mt-0.5">
					<p class="text-xs text-slate-400">{relativeTime(session.last_event_ts)}</p>
					{#if session.context_name}
						<span class="text-xs px-1.5 py-0.5 rounded-md bg-vanna-teal/10 text-vanna-teal font-medium truncate max-w-[120px]">@{session.context_name}</span>
					{/if}
				</div>
			</div>
		</a>

		<button
			type="button"
			onclick={() => onDelete(session.id)}
			class="opacity-0 group-hover:opacity-100 p-1.5 text-slate-300 hover:text-red-400 rounded-xl transition-all flex-shrink-0"
			title="Delete session"
		>
			<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
				<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
					d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
				/>
			</svg>
		</button>
	</div>

	<!-- Agent stats row -->
	<div class="flex items-center gap-3 mb-4 text-xs">
		<span class="text-slate-500">
			<span class="font-medium text-vanna-navy">{session.agent_count}</span> agent{session.agent_count !== 1 ? 's' : ''}
		</span>
		{#if session.live_agents > 0}
			<span class="text-vanna-teal font-medium">
				{session.live_agents} live
			</span>
		{/if}
		{#if session.stuck_agents > 0}
			<span class="text-red-500 font-medium">
				{session.stuck_agents} stuck
			</span>
		{/if}
		{#if session.waiting_agents > 0}
			<span class="text-orange-500 font-medium">
				{session.waiting_agents} waiting
			</span>
		{/if}
		{#if totalTasks > 0}
			<span class="text-slate-400 ml-auto">
				{totalTasks} task{totalTasks !== 1 ? 's' : ''}
			</span>
		{/if}
	</div>

	<!-- Mini kanban bar -->
	<KanbanMiniBar tasksByStage={session.tasks_by_stage} />
</div>
