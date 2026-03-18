<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import IsoRoom from '$lib/components/swarm/IsoRoom.svelte';

	interface Agent {
		id: string;
		name: string;
		role: string;
		status: string;
		tmux_session?: string | null;
		current_task_id?: string | null;
		latest_note?: string | null;
	}

	interface Task {
		id: string;
		title: string;
		stage: string;
	}

	interface SwarmState {
		session: { id: string; name: string };
		agents: Agent[];
		tasks: Task[];
	}

	// Cycling room border colours — one per session slot
	const ROOM_COLORS = ['#14b8a6', '#a855f7', '#f97316', '#3b82f6', '#ec4899', '#eab308'];

	let sessionData = $state<Record<string, SwarmState>>({});
	let sessionColors: Record<string, string> = {};
	let colorIdx = 0;

	const sockets = new Map<string, WebSocket>();
	let pollTimer: ReturnType<typeof setInterval> | null = null;
	let connected = $state(false);

	// ── Derived stats ──────────────────────────────────────────
	let allAgents    = $derived(Object.values(sessionData).flatMap((s) => s.agents));
	let liveCount    = $derived(allAgents.filter((a) => !!a.tmux_session).length);
	let codingCount  = $derived(allAgents.filter((a) => a.status === 'coding').length);
	let thinkingCount= $derived(allAgents.filter((a) => a.status === 'thinking').length);
	let stuckCount   = $derived(allAgents.filter((a) => a.status === 'stuck').length);
	let waitingCount = $derived(allAgents.filter((a) => a.status === 'waiting').length);
	let sessions     = $derived(Object.values(sessionData));
	let hasStuck     = $derived(stuckCount > 0);

	function connectSession(sessionId: string) {
		if (sockets.has(sessionId)) return;
		if (!(sessionId in sessionColors)) {
			sessionColors[sessionId] = ROOM_COLORS[colorIdx % ROOM_COLORS.length];
			colorIdx++;
		}
		const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
		const ws = new WebSocket(`${proto}//${window.location.host}/ws/swarm?session=${sessionId}`);
		ws.onopen = () => { connected = true; };
		ws.onmessage = (e) => {
			try {
				const msg = JSON.parse(e.data);
				if (msg.type === 'swarm_state' && msg.state) {
					sessionData = { ...sessionData, [sessionId]: msg.state };
				}
			} catch {}
		};
		ws.onclose = () => {
			sockets.delete(sessionId);
			setTimeout(() => connectSession(sessionId), 5000);
		};
		sockets.set(sessionId, ws);
	}

	async function refreshSessions() {
		try {
			const res = await fetch('/api/swarm/dashboard');
			if (!res.ok) return;
			const data = await res.json();
			for (const s of data.sessions ?? []) connectSession(s.id);
		} catch {}
	}

	onMount(async () => {
		await refreshSessions();
		pollTimer = setInterval(refreshSessions, 15_000);
	});

	onDestroy(() => {
		if (pollTimer) clearInterval(pollTimer);
		for (const ws of sockets.values()) ws.close();
	});
</script>

<svelte:head>
	<title>Swarm Live — SwarmOps</title>
</svelte:head>

<div class="floor-wrap">

	<!-- ── HUD ────────────────────────────────────────────────── -->
	<header class="hud {hasStuck ? 'hud-alert' : ''}">
		<div class="hud-left">
			<span class="hud-title">RC SWARM LIVE</span>
			<span class="hud-sep">│</span>
			<a href="/swarm" class="hud-back">← Sessions</a>
		</div>
		<div class="hud-stats">
			{#if liveCount > 0}
				<span class="stat stat-live">
					<span class="stat-dot live-dot"></span>
					{liveCount} live
				</span>
			{/if}
			{#if codingCount > 0}
				<span class="stat stat-coding">⚡ {codingCount} coding</span>
			{/if}
			{#if thinkingCount > 0}
				<span class="stat stat-thinking">💭 {thinkingCount} thinking</span>
			{/if}
			{#if waitingCount > 0}
				<span class="stat stat-waiting">⏸ {waitingCount} waiting</span>
			{/if}
			{#if stuckCount > 0}
				<span class="stat stat-stuck">⚠ {stuckCount} stuck</span>
			{/if}
			{#if liveCount === 0 && allAgents.length === 0}
				<span class="stat stat-idle">No active agents</span>
			{/if}
		</div>
	</header>

	<!-- ── Office Floor ──────────────────────────────────────── -->
	<main class="floor">
		{#if sessions.length === 0}
			<div class="empty-floor">
				<p class="empty-title">No swarm sessions</p>
				<a href="/swarm" class="empty-link">Create one →</a>
			</div>
		{:else}
			{#each sessions as s (s.session.id)}
				{@const color = sessionColors[s.session.id] ?? '#14b8a6'}
				<IsoRoom
					sessionId={s.session.id}
					sessionName={s.session.name}
					agents={s.agents}
					tasks={s.tasks}
					{color}
				/>
			{/each}
		{/if}
	</main>
</div>

<style>
	/* ── Reset + base ─────────────────────────────────────────── */
	:global(body) {
		background: #060d1a !important;
	}

	.floor-wrap {
		min-height: 100vh;
		background: #060d1a;
		background-image: radial-gradient(circle, rgba(255,255,255,0.03) 1px, transparent 1px);
		background-size: 28px 28px;
		display: flex;
		flex-direction: column;
		font-family: 'Inter', ui-sans-serif, system-ui, sans-serif;
		color: #e2e8f0;
	}

	/* ── HUD ──────────────────────────────────────────────────── */
	.hud {
		position: sticky;
		top: 0;
		z-index: 200;
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 10px 20px;
		background: rgba(6, 13, 26, 0.92);
		border-bottom: 1px solid rgba(255,255,255,0.07);
		backdrop-filter: blur(8px);
		transition: border-color 0.3s, box-shadow 0.3s;
	}
	.hud-alert {
		border-bottom-color: rgba(239,68,68,0.4);
		box-shadow: 0 1px 20px rgba(239,68,68,0.15);
		animation: hud-pulse 1.5s ease-in-out infinite;
	}
	@keyframes hud-pulse {
		0%, 100% { box-shadow: 0 1px 20px rgba(239,68,68,0.1); }
		50%       { box-shadow: 0 1px 30px rgba(239,68,68,0.3); }
	}
	.hud-left   { display: flex; align-items: center; gap: 12px; }
	.hud-title  { font-size: 13px; font-weight: 700; letter-spacing: 0.12em; color: #14b8a6; }
	.hud-sep    { color: rgba(255,255,255,0.15); }
	.hud-back   { font-size: 12px; color: rgba(255,255,255,0.35); text-decoration: none; }
	.hud-back:hover { color: #14b8a6; }

	.hud-stats  { display: flex; align-items: center; gap: 14px; }
	.stat       { font-size: 12px; font-weight: 500; display: flex; align-items: center; gap: 5px; }
	.stat-live     { color: #14b8a6; }
	.stat-coding   { color: #4ade80; }
	.stat-thinking { color: #60a5fa; }
	.stat-waiting  { color: #f97316; }
	.stat-stuck    { color: #ef4444; animation: blink-stat 0.8s step-end infinite; }
	.stat-idle     { color: rgba(255,255,255,0.25); }
	@keyframes blink-stat { 0%,100%{opacity:1} 50%{opacity:0.4} }

	.stat-dot  { width: 6px; height: 6px; border-radius: 50%; }
	.live-dot  { background: #14b8a6; animation: dot-pulse 1.5s ease-in-out infinite; }
	@keyframes dot-pulse {
		0%,100%{ box-shadow: 0 0 0 0 rgba(20,184,166,0.4); }
		50%    { box-shadow: 0 0 0 5px rgba(20,184,166,0); }
	}

	/* ── Floor ─────────────────────────────────────────────── */
	.floor {
		flex: 1;
		padding: 28px 24px 40px;
		display: flex;
		flex-direction: column;
		gap: 28px;
	}

	.empty-floor {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		min-height: 60vh;
		gap: 12px;
	}
	.empty-title { color: rgba(255,255,255,0.2); font-size: 15px; }
	.empty-link  { color: #14b8a6; text-decoration: none; font-size: 13px; }
</style>
