<script lang="ts">
	import { onMount } from 'svelte';
	import Card from '$lib/components/ui/Card.svelte';
	import Badge from '$lib/components/ui/Badge.svelte';
	import Button from '$lib/components/ui/Button.svelte';

	interface Milestone {
		id: number;
		text: string;
		created_at: string;
	}

	interface Execution {
		id: number;
		task_id: number;
		agent_id: number;
		status: string;
		task_title: string;
		agent_name: string;
		project_id: number;
		project_name: string;
		agent_tmux_id: { String: string; Valid: boolean } | string | null;
		created_at: string;
		milestones?: Milestone[];
	}

	interface TmuxSession {
		name: string;
		preview: string;
		is_task: boolean;
		task_id?: number;
		agent_id?: number;
		execution_id?: number;
	}

	let executions = $state<Execution[]>([]);
	let tmuxSessions = $state<TmuxSession[]>([]);
	let loading = $state(true);
	let showRecent = $state(false);
	let expandedTerminals = $state<Set<number>>(new Set());

	onMount(async () => {
		await loadData();
		const interval = setInterval(loadData, 5000);
		return () => clearInterval(interval);
	});

	async function loadData() {
		try {
			const [execRes, tmuxRes] = await Promise.all([
				fetch('/api/task-executions?include_milestones=true'),
				fetch('/api/tmux-sessions')
			]);
			if (execRes.ok) executions = await execRes.json();
			if (tmuxRes.ok) tmuxSessions = await tmuxRes.json();
		} catch (error) {
			console.error('Failed to load kanban data:', error);
		} finally {
			loading = false;
		}
	}

	// Build tmux session lookup by task_id
	let sessionByTaskId = $derived(() => {
		const map = new Map<number, TmuxSession>();
		for (const s of tmuxSessions) {
			if (s.task_id) map.set(s.task_id, s);
		}
		return map;
	});

	// Categorize executions into columns
	let running = $derived(executions.filter(e => e.status === 'running'));
	let waiting = $derived(executions.filter(e => e.status === 'Waiting' || e.status === 'waiting'));
	let recent = $derived(
		executions
			.filter(e => ['completed', 'rejected', 'failed'].includes(e.status))
			.slice(0, 5)
	);

	function getStatusColor(status: string) {
		switch (status?.toLowerCase()) {
			case 'completed': return 'success';
			case 'running': return 'primary';
			case 'waiting': return 'warning';
			case 'failed': case 'rejected': return 'danger';
			default: return 'secondary';
		}
	}

	function formatTimeAgo(dateString?: string) {
		if (!dateString) return '';
		const date = new Date(dateString);
		const now = new Date();
		const diffMs = now.getTime() - date.getTime();
		const diffMin = Math.floor(diffMs / 60000);
		if (diffMin < 1) return 'Just now';
		if (diffMin < 60) return `${diffMin}m ago`;
		if (diffMin < 1440) return `${Math.floor(diffMin / 60)}h ago`;
		return `${Math.floor(diffMin / 1440)}d ago`;
	}

	function getTerminalSnippet(execution: Execution): string {
		const session = sessionByTaskId().get(execution.task_id);
		if (!session?.preview) return '';
		// Strip HTML tags and get last 6 lines
		const text = session.preview.replace(/<[^>]+>/g, '').trim();
		const lines = text.split('\n').filter((l: string) => l.trim());
		return lines.slice(-6).join('\n');
	}

	function toggleTerminal(id: number) {
		const next = new Set(expandedTerminals);
		if (next.has(id)) next.delete(id);
		else next.add(id);
		expandedTerminals = next;
	}
</script>

<svelte:head>
	<title>Kanban - Remote-Code</title>
</svelte:head>

<div class="space-y-6">
	<!-- Page Header -->
	<div class="flex items-center justify-between mb-8">
		<div>
			<h1 class="text-3xl font-bold text-vanna-navy font-serif">Agent Activity</h1>
			<p class="mt-2 text-slate-500">Live view of what each agent is working on</p>
		</div>
		<div class="flex items-center gap-3">
			{#if recent.length > 0}
				<Button
					variant="ghost"
					size="sm"
					onclick={() => showRecent = !showRecent}
				>
					{showRecent ? 'Hide' : 'Show'} Recent ({recent.length})
				</Button>
			{/if}
			<div class="flex items-center gap-2 text-sm text-slate-500">
				<div class="w-2 h-2 bg-vanna-teal rounded-full animate-pulse"></div>
				Polling every 5s
			</div>
		</div>
	</div>

	{#if loading}
		<div class="text-center py-12 text-slate-500">Loading agent activity...</div>
	{:else if running.length === 0 && waiting.length === 0 && recent.length === 0}
		<Card>
			<div class="text-center py-12">
				<svg class="w-12 h-12 mx-auto text-slate-300 mb-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z"/>
				</svg>
				<p class="text-slate-500 text-lg">No active agents</p>
				<p class="text-slate-400 text-sm mt-1">Start a task execution to see agent activity here</p>
			</div>
		</Card>
	{:else}
		<!-- Kanban Columns -->
		<div class="grid grid-cols-1 lg:grid-cols-2 xl:grid-cols-3 gap-6">
			<!-- Running Column -->
			<div class="space-y-4">
				<div class="flex items-center gap-2">
					<div class="w-2 h-2 bg-vanna-teal rounded-full animate-pulse"></div>
					<h2 class="text-lg font-semibold text-vanna-navy">Running</h2>
					{#if running.length > 0}
						<span class="px-2 py-0.5 text-xs font-medium bg-vanna-teal/10 text-vanna-teal rounded-full">
							{running.length}
						</span>
					{/if}
				</div>

				{#if running.length === 0}
					<div class="bg-white/60 border border-dashed border-slate-200 rounded-2xl p-6 text-center text-sm text-slate-400">
						No agents running
					</div>
				{/if}

				{#each running as execution (execution.id)}
					{@const snippet = getTerminalSnippet(execution)}
					<div class="bg-white/80 backdrop-blur-sm rounded-2xl border border-slate-200/60 p-4 shadow-vanna-card hover:shadow-vanna-feature transition-all duration-200">
						<!-- Header -->
						<div class="flex items-start justify-between mb-2">
							<div class="flex-1 min-w-0">
								<div class="text-xs font-semibold text-vanna-teal uppercase tracking-wide mb-1">
									{execution.project_name}
								</div>
								<h4 class="text-sm font-medium text-vanna-navy line-clamp-2">
									{execution.task_title || `Task ${execution.task_id}`}
								</h4>
							</div>
							<Badge variant="primary" size="sm">
								<div class="w-2 h-2 bg-current rounded-full mr-1 animate-pulse"></div>
								Running
							</Badge>
						</div>

						<!-- Agent & Time -->
						<div class="flex items-center gap-3 text-xs text-slate-500 mb-3">
							<span class="flex items-center gap-1">
								<svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
									<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z"/>
								</svg>
								{execution.agent_name}
							</span>
							<span>{formatTimeAgo(execution.created_at)}</span>
						</div>

						<!-- Milestones -->
						{#if execution.milestones && execution.milestones.length > 0}
							<div class="mb-3 space-y-1">
								<div class="text-xs font-medium text-slate-400 uppercase tracking-wide">Activity</div>
								{#each execution.milestones.slice(0, 3) as milestone}
									<div class="flex items-start gap-1.5 text-xs text-slate-600">
										<span class="text-vanna-teal mt-0.5">&#8226;</span>
										<span class="line-clamp-1">{milestone.text}</span>
									</div>
								{/each}
								{#if execution.milestones.length > 3}
									<div class="text-xs text-slate-400">+{execution.milestones.length - 3} more</div>
								{/if}
							</div>
						{/if}

						<!-- Terminal Preview Toggle -->
						{#if snippet}
							<button
								onclick={() => toggleTerminal(execution.id)}
								class="w-full text-left text-xs text-slate-400 hover:text-vanna-teal transition-colors flex items-center gap-1 mb-2"
							>
								<svg class="w-3 h-3 transition-transform {expandedTerminals.has(execution.id) ? 'rotate-90' : ''}" fill="none" stroke="currentColor" viewBox="0 0 24 24">
									<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7"/>
								</svg>
								Terminal
							</button>
							{#if expandedTerminals.has(execution.id)}
								<div class="bg-slate-900 text-green-400 text-xs font-mono p-3 rounded-lg overflow-x-auto max-h-32 overflow-y-auto whitespace-pre leading-relaxed">
									{snippet}
								</div>
							{/if}
						{/if}

						<!-- Actions -->
						<div class="flex items-center gap-2 mt-3 pt-3 border-t border-slate-100">
							<a href="/task-executions/{execution.id}" class="text-xs text-vanna-teal hover:text-vanna-teal/80 font-medium">
								View Details
							</a>
							<a href="/terminal" class="text-xs text-slate-400 hover:text-slate-600 font-medium">
								Full Terminal
							</a>
						</div>
					</div>
				{/each}
			</div>

			<!-- Waiting Column -->
			<div class="space-y-4">
				<div class="flex items-center gap-2">
					<svg class="w-4 h-4 text-vanna-orange animate-pulse" fill="currentColor" viewBox="0 0 20 20">
						<path fill-rule="evenodd" d="M8.257 3.099c.765-1.36 2.722-1.36 3.486 0l5.58 9.92c.75 1.334-.213 2.98-1.742 2.98H4.42c-1.53 0-2.493-1.646-1.743-2.98l5.58-9.92zM11 13a1 1 0 11-2 0 1 1 0 012 0zm-1-8a1 1 0 00-1 1v3a1 1 0 002 0V6a1 1 0 00-1-1z" clip-rule="evenodd"/>
					</svg>
					<h2 class="text-lg font-semibold text-vanna-navy">Waiting for Input</h2>
					{#if waiting.length > 0}
						<span class="px-2 py-0.5 text-xs font-medium bg-vanna-orange/10 text-vanna-orange rounded-full">
							{waiting.length}
						</span>
					{/if}
				</div>

				{#if waiting.length === 0}
					<div class="bg-white/60 border border-dashed border-slate-200 rounded-2xl p-6 text-center text-sm text-slate-400">
						No agents waiting
					</div>
				{/if}

				{#each waiting as execution (execution.id)}
					{@const snippet = getTerminalSnippet(execution)}
					<div class="bg-white/80 backdrop-blur-sm rounded-2xl border border-vanna-orange/30 p-4 shadow-vanna-card">
						<!-- Header -->
						<div class="flex items-start justify-between mb-2">
							<div class="flex-1 min-w-0">
								<div class="text-xs font-semibold text-vanna-teal uppercase tracking-wide mb-1">
									{execution.project_name}
								</div>
								<h4 class="text-sm font-medium text-vanna-navy line-clamp-2">
									{execution.task_title || `Task ${execution.task_id}`}
								</h4>
							</div>
							<Badge variant="warning" size="sm">
								<svg class="w-3 h-3 mr-1 animate-pulse" fill="currentColor" viewBox="0 0 20 20">
									<path fill-rule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zm1-12a1 1 0 10-2 0v4a1 1 0 00.293.707l2.828 2.829a1 1 0 101.415-1.415L11 9.586V6z" clip-rule="evenodd"/>
								</svg>
								Waiting
							</Badge>
						</div>

						<!-- Agent & Time -->
						<div class="flex items-center gap-3 text-xs text-slate-500 mb-3">
							<span class="flex items-center gap-1">
								<svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
									<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z"/>
								</svg>
								{execution.agent_name}
							</span>
							<span>{formatTimeAgo(execution.created_at)}</span>
						</div>

						<!-- Milestones -->
						{#if execution.milestones && execution.milestones.length > 0}
							<div class="mb-3 space-y-1">
								<div class="text-xs font-medium text-slate-400 uppercase tracking-wide">Activity</div>
								{#each execution.milestones.slice(0, 3) as milestone}
									<div class="flex items-start gap-1.5 text-xs text-slate-600">
										<span class="text-vanna-orange mt-0.5">&#8226;</span>
										<span class="line-clamp-1">{milestone.text}</span>
									</div>
								{/each}
							</div>
						{/if}

						<!-- Terminal Preview (always show for waiting — user needs context) -->
						{#if snippet}
							<div class="bg-slate-900 text-green-400 text-xs font-mono p-3 rounded-lg overflow-x-auto max-h-32 overflow-y-auto whitespace-pre leading-relaxed mb-3">
								{snippet}
							</div>
						{/if}

						<!-- Actions -->
						<div class="flex items-center gap-2 pt-3 border-t border-slate-100">
							<a href="/task-executions/{execution.id}" class="text-xs text-vanna-orange hover:text-vanna-orange/80 font-medium">
								Check Session
							</a>
							<a href="/terminal" class="text-xs text-slate-400 hover:text-slate-600 font-medium">
								Full Terminal
							</a>
						</div>
					</div>
				{/each}
			</div>

			<!-- Recent Column (toggled) -->
			{#if showRecent && recent.length > 0}
				<div class="space-y-4">
					<div class="flex items-center gap-2">
						<svg class="w-4 h-4 text-slate-400" fill="none" stroke="currentColor" viewBox="0 0 24 24">
							<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"/>
						</svg>
						<h2 class="text-lg font-semibold text-vanna-navy">Recent</h2>
					</div>

					{#each recent as execution (execution.id)}
						<div class="bg-white/60 backdrop-blur-sm rounded-2xl border border-slate-200/60 p-4 opacity-75">
							<div class="flex items-start justify-between mb-2">
								<div class="flex-1 min-w-0">
									<div class="text-xs font-semibold text-slate-400 uppercase tracking-wide mb-1">
										{execution.project_name}
									</div>
									<h4 class="text-sm font-medium text-slate-600 line-clamp-1">
										{execution.task_title || `Task ${execution.task_id}`}
									</h4>
								</div>
								<Badge variant={getStatusColor(execution.status)} size="sm">
									{execution.status}
								</Badge>
							</div>
							<div class="flex items-center gap-3 text-xs text-slate-400">
								<span>{execution.agent_name}</span>
								<span>{formatTimeAgo(execution.created_at)}</span>
							</div>
						</div>
					{/each}
				</div>
			{/if}
		</div>
	{/if}
</div>
