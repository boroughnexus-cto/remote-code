<script lang="ts">
	import IsoWorker from './IsoWorker.svelte';

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

	interface Props {
		sessionId: string;
		sessionName: string;
		agents: Agent[];
		tasks: Task[];
		color: string;
	}

	let { sessionId, sessionName, agents, tasks, color }: Props = $props();

	// Tile dimensions for iso projection
	const TW = 80;
	const TH = 40;
	// Grid columns
	const COLS = 4;

	// Assign each agent a grid position
	let agentTiles = $derived(
		agents.map((a, i) => ({
			agent: a,
			col: i % COLS,
			row: Math.floor(i / COLS)
		}))
	);

	// How many rows we need
	let rows = $derived(Math.max(1, Math.ceil(agents.length / COLS)));

	// Canvas size needed for the iso grid
	// Width: (COLS + rows - 1) * TW/2 * 2 = (COLS + rows - 1) * TW
	// Height: (COLS + rows) * TH/2 + 80 (name tags + workers above)
	let canvasW = $derived((COLS + rows) * TW + 40);
	let canvasH = $derived((COLS + rows) * TH + 140);

	// Floor tile grid — render decorative floor tiles
	let floorTiles = $derived(
		Array.from({ length: rows }, (_, r) =>
			Array.from({ length: COLS }, (_, c) => ({ col: c, row: r }))
		).flat()
	);

	function tileToScreen(c: number, r: number) {
		return {
			x: (c - r) * (TW / 2) + canvasW / 2 - TW / 2,
			y: (c + r) * (TH / 2) + 60
		};
	}

	let hasStuck = $derived(agents.some((a) => a.status === 'stuck'));
	let liveCount = $derived(agents.filter((a) => !!a.tmux_session).length);
</script>

<section class="iso-room {hasStuck ? 'room-alert' : ''}" style="--rc: {color}">
	<!-- Room header strip -->
	<div class="room-header">
		<span class="room-name">{sessionName}</span>
		<div class="room-pills">
			{#if liveCount > 0}
				<span class="pill pill-live">
					<span class="live-dot"></span>{liveCount} live
				</span>
			{/if}
			{#if hasStuck}
				<span class="pill pill-stuck">⚠ stuck</span>
			{/if}
			{#if agents.length === 0}
				<span class="pill pill-empty">empty</span>
			{/if}
		</div>
	</div>

	<!-- ISO canvas -->
	<div class="iso-canvas-wrap">
		<div class="iso-canvas" style="width: {canvasW}px; height: {canvasH}px">
			<!-- Floor tiles -->
			{#each floorTiles as tile}
				{@const pos = tileToScreen(tile.col, tile.row)}
				{@const zIdx = tile.row * 100 + tile.col}
				<svg
					class="floor-tile"
					width={TW + 2}
					height={TH + 2}
					viewBox="-1 -1 {TW + 2} {TH + 2}"
					style="left: {pos.x - TW/2}px; top: {pos.y - TH/2}px; z-index: {zIdx - 1}"
				>
					<polygon
						points="{TW/2},1 {TW},{ TH/2} {TW/2},{TH} 0,{TH/2}"
						fill="rgba(255,255,255,0.02)"
						stroke={color}
						stroke-opacity="0.12"
						stroke-width="0.5"
					/>
				</svg>
			{/each}

			<!-- Workers -->
			{#each agentTiles as at (at.agent.id)}
				{@const pos = tileToScreen(at.col, at.row)}
				<div
					class="worker-wrap"
					style="left: {pos.x}px; top: {pos.y}px; z-index: {at.row * 100 + at.col + 10}"
				>
					<IsoWorker agent={at.agent} {tasks} col={0} row={0} />
				</div>
			{/each}

			<!-- Empty floor message -->
			{#if agents.length === 0}
				<div class="empty-msg" style="top: {canvasH / 2 - 20}px">
					No agents — <a href="/swarm/{sessionId}">add one</a>
				</div>
			{/if}
		</div>
	</div>
</section>

<style>
	.iso-room {
		background: rgba(255,255,255,0.015);
		border: 1px solid rgba(255,255,255,0.06);
		border-top: 2px solid var(--rc);
		border-radius: 10px;
		overflow: hidden;
		transition: border-top-color 0.3s, box-shadow 0.3s;
	}
	.room-alert {
		border-top-color: #ef4444 !important;
		animation: room-alert 2s ease-in-out infinite;
	}
	@keyframes room-alert {
		0%, 100% { box-shadow: 0 0 0 0 transparent; }
		50%       { box-shadow: 0 0 24px rgba(239,68,68,0.25); }
	}

	.room-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		padding: 10px 16px;
		border-bottom: 1px solid rgba(255,255,255,0.05);
		background: rgba(255,255,255,0.02);
	}
	.room-name {
		font-size: 11px;
		font-weight: 700;
		color: var(--rc);
		letter-spacing: 0.1em;
		text-transform: uppercase;
	}
	.room-pills { display: flex; gap: 6px; align-items: center; }
	.pill {
		font-size: 10px;
		font-weight: 600;
		padding: 2px 8px;
		border-radius: 20px;
		display: flex;
		align-items: center;
		gap: 4px;
	}
	.pill-live  { background: rgba(20,184,166,0.15); color: #14b8a6; }
	.pill-stuck { background: rgba(239,68,68,0.15); color: #ef4444; animation: blink 0.8s step-end infinite; }
	.pill-empty { background: rgba(255,255,255,0.05); color: rgba(255,255,255,0.25); }
	@keyframes blink { 0%,100%{opacity:1} 50%{opacity:0.3} }

	.live-dot {
		width: 5px; height: 5px;
		border-radius: 50%;
		background: #14b8a6;
		animation: dot-pulse 1.5s ease-in-out infinite;
		flex-shrink: 0;
	}
	@keyframes dot-pulse {
		0%,100%{ box-shadow: 0 0 0 0 rgba(20,184,166,0.4); }
		50%    { box-shadow: 0 0 0 4px rgba(20,184,166,0); }
	}

	/* ── ISO canvas ── */
	.iso-canvas-wrap {
		overflow-x: auto;
		overflow-y: visible;
		padding: 8px 0 16px;
	}
	.iso-canvas {
		position: relative;
		margin: 0 auto;
		image-rendering: pixelated;
	}

	.floor-tile {
		position: absolute;
		image-rendering: pixelated;
	}

	.worker-wrap {
		position: absolute;
		transform: translate(-44px, -30px);
	}

	.empty-msg {
		position: absolute;
		left: 50%;
		transform: translateX(-50%);
		font-size: 11px;
		color: rgba(255,255,255,0.2);
		font-style: italic;
		white-space: nowrap;
	}
	.empty-msg a { color: #14b8a6; text-decoration: none; }
</style>
