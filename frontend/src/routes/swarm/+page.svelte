<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { goto } from '$app/navigation';
	import DashboardSessionCard from '$lib/components/swarm/DashboardSessionCard.svelte';

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

	let sessions = $state<SessionStats[]>([]);
	let loading = $state(true);
	let creating = $state(false);
	let newName = $state('');
	let showForm = $state(false);
	let error = $state('');
	let pollTimer: ReturnType<typeof setInterval> | null = null;

	onMount(async () => {
		await loadDashboard();
		// Poll every 10s to reflect live/stuck/waiting changes
		pollTimer = setInterval(loadDashboard, 10_000);
	});

	onDestroy(() => {
		if (pollTimer) clearInterval(pollTimer);
	});

	async function loadDashboard() {
		try {
			const res = await fetch('/api/swarm/dashboard');
			if (res.ok) {
				const data = await res.json();
				sessions = data.sessions ?? [];
			}
		} catch (e) {
			error = 'Failed to load sessions';
		} finally {
			loading = false;
		}
	}

	async function createSession() {
		if (!newName.trim()) return;
		creating = true;
		error = '';
		try {
			const res = await fetch('/api/swarm/sessions', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ name: newName.trim() })
			});
			if (res.ok) {
				const session = await res.json();
				goto(`/swarm/${session.id}`);
			} else {
				error = 'Failed to create session';
				creating = false;
			}
		} catch (e) {
			error = 'Failed to create session';
			creating = false;
		}
	}

	async function deleteSession(id: string) {
		await fetch(`/api/swarm/sessions/${id}`, { method: 'DELETE' });
		sessions = sessions.filter((s) => s.id !== id);
	}

	let liveCount = $derived(sessions.filter((s) => s.live_agents > 0).length);
	let stuckCount = $derived(sessions.reduce((n, s) => n + s.stuck_agents, 0));
</script>

<svelte:head>
	<title>Swarm — SwarmOps</title>
</svelte:head>

<div class="max-w-5xl mx-auto">
	<!-- Header -->
	<div class="flex items-center justify-between mb-6">
		<div>
			<h1 class="text-2xl font-bold text-vanna-navy">Swarm Orchestrator</h1>
			<p class="text-sm text-slate-500 mt-1">Coordinate multiple Claude Code agents across your projects</p>
		</div>
		<div class="flex items-center gap-2">
			<a
				href="/swarm/live"
				class="flex items-center gap-1.5 px-3 py-2 rounded-xl border border-vanna-teal/30 text-vanna-teal text-sm font-medium hover:bg-vanna-teal/10 transition-colors"
			>
				<span class="w-2 h-2 rounded-full bg-vanna-teal animate-pulse"></span>
				Live View
			</a>
			<button
				type="button"
				onclick={() => (showForm = !showForm)}
				class="flex items-center gap-2 bg-vanna-teal text-white px-4 py-2 rounded-xl font-medium text-sm hover:bg-vanna-teal/90 transition-colors shadow-vanna-subtle"
			>
				<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
				</svg>
				New Swarm
			</button>
		</div>
	</div>

	<!-- Summary bar -->
	{#if sessions.length > 0}
		<div class="flex items-center gap-4 mb-6 text-sm">
			<span class="text-slate-500">{sessions.length} session{sessions.length !== 1 ? 's' : ''}</span>
			{#if liveCount > 0}
				<span class="flex items-center gap-1.5 text-vanna-teal font-medium">
					<span class="w-2 h-2 rounded-full bg-vanna-teal animate-pulse"></span>
					{liveCount} active
				</span>
			{/if}
			{#if stuckCount > 0}
				<span class="flex items-center gap-1.5 text-red-500 font-medium">
					<span class="w-2 h-2 rounded-full bg-red-500"></span>
					{stuckCount} stuck
				</span>
			{/if}
		</div>
	{/if}

	<!-- Create form -->
	{#if showForm}
		<div class="bg-white/80 rounded-2xl border border-slate-200 shadow-vanna-card p-6 mb-6">
			<h2 class="font-semibold text-vanna-navy mb-4">Create New Swarm Session</h2>
			<form
				onsubmit={(e) => { e.preventDefault(); createSession(); }}
				class="flex gap-3"
			>
				<input
					type="text"
					bind:value={newName}
					placeholder="e.g. Feature: User Auth Redesign"
					class="flex-1 px-4 py-2.5 rounded-xl border border-slate-200 bg-white text-sm text-vanna-navy placeholder-slate-400 focus:outline-none focus:ring-2 focus:ring-vanna-teal/40 focus:border-vanna-teal transition-all"
					autofocus
				/>
				<button
					type="submit"
					disabled={creating || !newName.trim()}
					class="bg-vanna-teal text-white px-5 py-2.5 rounded-xl font-medium text-sm disabled:opacity-50 hover:bg-vanna-teal/90 transition-colors"
				>
					{creating ? 'Creating…' : 'Create'}
				</button>
				<button
					type="button"
					onclick={() => (showForm = false)}
					class="px-4 py-2.5 rounded-xl border border-slate-200 text-sm text-slate-500 hover:bg-vanna-cream/50 transition-colors"
				>
					Cancel
				</button>
			</form>
		</div>
	{/if}

	{#if error}
		<div class="bg-red-50 border border-red-200 rounded-xl p-4 mb-6 text-sm text-red-600">{error}</div>
	{/if}

	<!-- Session grid -->
	{#if loading}
		<div class="flex items-center justify-center py-16">
			<div class="animate-spin rounded-full h-8 w-8 border-b-2 border-vanna-teal"></div>
		</div>
	{:else if sessions.length === 0}
		<div class="text-center py-20">
			<div class="w-16 h-16 bg-vanna-teal/10 rounded-2xl flex items-center justify-center mx-auto mb-4">
				<svg class="w-8 h-8 text-vanna-teal/60" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5"
						d="M17 20h5v-2a3 3 0 00-5.356-1.857M17 20H7m10 0v-2c0-.656-.126-1.283-.356-1.857M7 20H2v-2a3 3 0 015.356-1.857M7 20v-2c0-.656.126-1.283.356-1.857m0 0a5.002 5.002 0 019.288 0M15 7a3 3 0 11-6 0 3 3 0 016 0z"
					/>
				</svg>
			</div>
			<p class="text-vanna-navy font-semibold mb-1">No swarm sessions yet</p>
			<p class="text-sm text-slate-400">Create a session to start orchestrating agents</p>
		</div>
	{:else}
		<div class="grid gap-4 sm:grid-cols-2">
			{#each sessions as session (session.id)}
				<DashboardSessionCard {session} onDelete={deleteSession} />
			{/each}
		</div>
	{/if}
</div>
