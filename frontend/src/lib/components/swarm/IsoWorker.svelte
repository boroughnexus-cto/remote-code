<script lang="ts">
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
		agent: Agent;
		tasks?: Task[];
		col: number;
		row: number;
	}

	let { agent, tasks = [], col, row }: Props = $props();

	// Isometric projection: tile size 80x40
	const TW = 80;
	const TH = 40;

	function tileToScreen(c: number, r: number) {
		return {
			x: (c - r) * (TW / 2),
			y: (c + r) * (TH / 2)
		};
	}

	const roleConfig: Record<string, { color: string; monitorColor: string; label: string }> = {
		orchestrator:   { color: '#ec4899', monitorColor: '#ec4899', label: 'Orchestrator' },
		'senior-dev':   { color: '#14b8a6', monitorColor: '#14b8a6', label: 'Senior Dev' },
		'qa-agent':     { color: '#eab308', monitorColor: '#eab308', label: 'QA' },
		'devops-agent': { color: '#3b82f6', monitorColor: '#3b82f6', label: 'DevOps' },
		researcher:     { color: '#a855f7', monitorColor: '#a855f7', label: 'Researcher' },
		worker:         { color: '#64748b', monitorColor: '#64748b', label: 'Worker' }
	};

	const roleEmoji: Record<string, string> = {
		orchestrator: '🧠', 'senior-dev': '🧑‍💻', 'qa-agent': '🔬',
		'devops-agent': '⚙️', researcher: '📚', worker: '👷'
	};

	const statusAnim: Record<string, string> = {
		coding:   'anim-typing',
		testing:  'anim-typing',
		thinking: 'anim-think',
		waiting:  'anim-idle',
		stuck:    'anim-panic',
		done:     'anim-idle',
		idle:     'anim-idle'
	};

	const statusGlow: Record<string, string> = {
		coding:   'rgba(74,222,128,0.5)',
		testing:  'rgba(234,179,8,0.5)',
		thinking: 'rgba(96,165,250,0.5)',
		waiting:  'rgba(249,115,22,0.4)',
		stuck:    'rgba(239,68,68,0.7)',
		done:     'rgba(74,222,128,0.3)',
		idle:     'rgba(100,116,139,0.2)'
	};

	let rc = $derived(roleConfig[agent.role] ?? roleConfig.worker);
	let emoji = $derived(roleEmoji[agent.role] ?? '👷');
	let anim = $derived(statusAnim[agent.status] ?? 'anim-idle');
	let glow = $derived(statusGlow[agent.status] ?? 'rgba(100,116,139,0.2)');
	let isLive = $derived(!!agent.tmux_session);
	let currentTask = $derived(tasks.find((t) => t.id === agent.current_task_id) ?? null);
	let terminalHref = $derived(isLive ? `/terminal/${agent.tmux_session}` : null);

	let pos = $derived(tileToScreen(col, row));
	// z-index: deeper tiles render in front
	let zIdx = $derived(row * 100 + col);

	function firstName(name: string) {
		return name.split(' ')[0];
	}

	// Thought bubble content
	let bubble = $derived(
		agent.status === 'thinking' ? '💭' :
		agent.status === 'stuck'    ? '!!' :
		agent.status === 'coding'   ? '</>' :
		agent.status === 'testing'  ? '🧪' :
		agent.status === 'done'     ? '✓'  : null
	);
</script>

<!-- Absolute-positioned iso tile + worker -->
<div
	class="iso-tile"
	style="
		left: {pos.x}px;
		top: {pos.y}px;
		z-index: {zIdx};
	"
>
	<!-- ISO DESK SVG -->
	<svg class="desk-svg" width="88" height="64" viewBox="0 0 88 64" fill="none">
		<!-- Desk top face -->
		<polygon points="44,4 84,24 44,44 4,24" fill={rc.color} fill-opacity="0.18" stroke={rc.color} stroke-opacity="0.5" stroke-width="1"/>
		<!-- Desk left face -->
		<polygon points="4,24 44,44 44,54 4,34" fill="rgba(0,0,0,0.45)" stroke={rc.color} stroke-opacity="0.3" stroke-width="0.5"/>
		<!-- Desk right face -->
		<polygon points="84,24 44,44 44,54 84,34" fill="rgba(0,0,0,0.3)" stroke={rc.color} stroke-opacity="0.3" stroke-width="0.5"/>

		<!-- Monitor on desk top -->
		<rect x="32" y="10" width="26" height="18" rx="2"
			fill={rc.monitorColor} fill-opacity="0.12"
			stroke={rc.monitorColor} stroke-opacity="0.7" stroke-width="1.2"
			transform="skewX(-28) translate(12,0)"
		/>
		<!-- Monitor screen glow -->
		<rect x="34" y="12" width="22" height="14" rx="1"
			fill={rc.monitorColor} fill-opacity="0.25"
			transform="skewX(-28) translate(12,0)"
		/>
		<!-- Monitor stand -->
		<rect x="43" y="28" width="3" height="6" fill={rc.color} fill-opacity="0.4"
			transform="skewX(-28) translate(12,0)"
		/>
	</svg>

	<!-- Worker character — sits on desk -->
	<a
		href={terminalHref ?? undefined}
		class="worker {anim} {isLive ? '' : 'worker-offline'}"
		style="--glow: {glow}; --rc: {rc.color}"
		onclick={(e) => { if (!terminalHref) e.preventDefault(); }}
		title="{agent.name} ({rc.label}) — {agent.status}{currentTask ? ' · ' + currentTask.title : ''}"
	>
		<span class="worker-emoji">{emoji}</span>
	</a>

	<!-- Thought bubble -->
	{#if bubble && isLive}
		<div class="bubble {agent.status === 'stuck' ? 'bubble-panic' : ''}">{bubble}</div>
	{/if}

	<!-- Monitor glow effect when coding/testing -->
	{#if (agent.status === 'coding' || agent.status === 'testing') && isLive}
		<div class="monitor-glow" style="--glow: {glow}"></div>
	{/if}

	<!-- Name tag -->
	<div class="nametag" style="--rc: {rc.color}">
		<span class="nametag-name">{firstName(agent.name)}</span>
		{#if currentTask}
			<span class="nametag-task">{currentTask.title}</span>
		{/if}
	</div>
</div>

<style>
	.iso-tile {
		position: absolute;
		/* Offset so the tile centre is at pos */
		transform: translate(-44px, -4px);
		width: 88px;
	}

	.desk-svg {
		display: block;
		image-rendering: pixelated;
	}

	/* ── Worker character ── */
	.worker {
		position: absolute;
		/* Sit roughly above the desk top face centre */
		top: -18px;
		left: 50%;
		transform: translateX(-50%);
		font-size: 22px;
		line-height: 1;
		text-decoration: none;
		cursor: default;
		filter: drop-shadow(0 2px 6px var(--glow));
		transition: filter 0.3s;
	}
	.worker.worker-offline {
		opacity: 0.35;
		filter: grayscale(80%);
	}
	.worker-emoji {
		display: block;
	}

	/* ── Status animations ── */
	.anim-typing {
		animation: worker-type 0.5s steps(2) infinite;
	}
	@keyframes worker-type {
		0%   { transform: translateX(-50%) translateY(0); }
		50%  { transform: translateX(-50%) translateY(-2px); }
		100% { transform: translateX(-50%) translateY(0); }
	}

	.anim-think {
		animation: worker-float 2s ease-in-out infinite;
	}
	@keyframes worker-float {
		0%, 100% { transform: translateX(-50%) translateY(0); }
		50%      { transform: translateX(-50%) translateY(-6px); }
	}

	.anim-idle {
		animation: worker-breathe 3s ease-in-out infinite;
	}
	@keyframes worker-breathe {
		0%, 100% { transform: translateX(-50%) scale(1); }
		50%      { transform: translateX(-50%) scale(1.04); }
	}

	.anim-panic {
		animation: worker-shake 0.25s ease-in-out infinite;
	}
	@keyframes worker-shake {
		0%, 100% { transform: translateX(-50%); }
		25%      { transform: translateX(calc(-50% - 3px)); }
		75%      { transform: translateX(calc(-50% + 3px)); }
	}

	/* ── Thought bubble ── */
	.bubble {
		position: absolute;
		top: -42px;
		left: 50%;
		transform: translateX(-50%);
		background: rgba(15, 23, 42, 0.85);
		border: 1px solid rgba(255,255,255,0.15);
		border-radius: 8px;
		font-size: 11px;
		padding: 2px 6px;
		white-space: nowrap;
		color: #e2e8f0;
		pointer-events: none;
		animation: bubble-float 2s ease-in-out infinite;
	}
	.bubble::after {
		content: '';
		position: absolute;
		bottom: -5px;
		left: 50%;
		transform: translateX(-50%);
		border: 3px solid transparent;
		border-top-color: rgba(255,255,255,0.15);
	}
	.bubble-panic {
		background: rgba(239,68,68,0.2);
		border-color: rgba(239,68,68,0.5);
		color: #ef4444;
		font-weight: 700;
		animation: bubble-blink 0.5s step-end infinite;
	}
	@keyframes bubble-float {
		0%, 100% { transform: translateX(-50%) translateY(0); }
		50%      { transform: translateX(-50%) translateY(-3px); }
	}
	@keyframes bubble-blink {
		0%, 100% { opacity: 1; }
		50%      { opacity: 0.3; }
	}

	/* ── Monitor glow overlay ── */
	.monitor-glow {
		position: absolute;
		top: 8px;
		left: 30px;
		width: 28px;
		height: 16px;
		background: var(--glow);
		border-radius: 2px;
		filter: blur(6px);
		pointer-events: none;
		animation: glow-pulse 1s ease-in-out infinite;
		transform: skewX(-28deg);
	}
	@keyframes glow-pulse {
		0%, 100% { opacity: 0.5; }
		50%      { opacity: 1; }
	}

	/* ── Name tag below desk ── */
	.nametag {
		position: absolute;
		bottom: -28px;
		left: 50%;
		transform: translateX(-50%);
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 1px;
		white-space: nowrap;
		pointer-events: none;
	}
	.nametag-name {
		font-size: 10px;
		font-weight: 600;
		color: var(--rc);
		letter-spacing: 0.03em;
	}
	.nametag-task {
		font-size: 8px;
		color: rgba(226,232,240,0.4);
		max-width: 80px;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>
