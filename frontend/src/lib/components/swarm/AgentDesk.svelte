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
	}

	let { agent, tasks = [] }: Props = $props();

	const roleConfig: Record<string, { color: string; emoji: string; label: string }> = {
		orchestrator:   { color: '#ec4899', emoji: '🧠', label: 'Orchestrator' },
		'senior-dev':   { color: '#14b8a6', emoji: '🧑‍💻', label: 'Senior Dev' },
		'qa-agent':     { color: '#eab308', emoji: '🔬', label: 'QA' },
		'devops-agent': { color: '#3b82f6', emoji: '⚙️',  label: 'DevOps' },
		researcher:     { color: '#a855f7', emoji: '📚', label: 'Researcher' },
		worker:         { color: '#64748b', emoji: '👷', label: 'Worker' }
	};

	const statusConfig: Record<string, { label: string; monitorBg: string; monitorFg: string; badge: string }> = {
		idle:     { label: 'Idle',      monitorBg: '#0f172a', monitorFg: '#334155', badge: '' },
		thinking: { label: 'Thinking…', monitorBg: '#0f1f3d', monitorFg: '#3b82f6', badge: '💭' },
		coding:   { label: 'Coding',    monitorBg: '#041a0e', monitorFg: '#22c55e', badge: '⚡' },
		testing:  { label: 'Testing',   monitorBg: '#1c1a04', monitorFg: '#eab308', badge: '🧪' },
		waiting:  { label: 'Waiting',   monitorBg: '#1c0f04', monitorFg: '#f97316', badge: '⏸' },
		stuck:    { label: 'Stuck!',    monitorBg: '#1c0404', monitorFg: '#ef4444', badge: '⚠️' },
		done:     { label: 'Done',      monitorBg: '#041c0a', monitorFg: '#4ade80', badge: '✅' }
	};

	let rc = $derived(roleConfig[agent.role] ?? roleConfig.worker);
	let sc = $derived(statusConfig[agent.status] ?? statusConfig.idle);
	let currentTask = $derived(tasks.find((t) => t.id === agent.current_task_id) ?? null);
	let isLive = $derived(!!agent.tmux_session);
	let terminalHref = $derived(isLive ? `/terminal/${agent.tmux_session}` : null);

	function firstName(name: string) {
		return name.split(' ')[0];
	}
</script>

<a
	href={terminalHref ?? undefined}
	class="desk status-{agent.status} {isLive ? 'is-live' : 'is-offline'}"
	style="--rc: {rc.color}; --mfg: {sc.monitorFg}; --mbg: {sc.monitorBg}"
	title="{agent.name} ({rc.label}) — {sc.label}{currentTask ? ' · ' + currentTask.title : ''}"
	onclick={(e) => { if (!terminalHref) e.preventDefault(); }}
>
	<!-- Activity badge (top-right) -->
	{#if sc.badge && isLive}
		<span class="badge">{sc.badge}</span>
	{/if}

	<!-- Character -->
	<div class="char">
		<span class="char-emoji">{rc.emoji}</span>
	</div>

	<!-- Monitor -->
	<div class="monitor">
		{#if agent.status === 'coding' || agent.status === 'testing'}
			<div class="code-lines">
				<div class="code-line l1"></div>
				<div class="code-line l2"></div>
				<div class="code-line l3"></div>
			</div>
		{:else if agent.status === 'thinking'}
			<div class="think-orb"></div>
		{:else if agent.status === 'stuck'}
			<div class="stuck-bang">!</div>
		{:else if agent.status === 'done'}
			<div class="done-mark">✓</div>
		{:else if agent.status === 'waiting'}
			<div class="wait-bar"></div>
		{/if}
	</div>

	<!-- Label -->
	<div class="label">
		<span class="name">{firstName(agent.name)}</span>
		<span class="status-text">{sc.label}</span>
	</div>

	<!-- Current task chip -->
	{#if currentTask}
		<div class="task-chip">{currentTask.title}</div>
	{/if}
</a>

<style>
	.desk {
		position: relative;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 4px;
		padding: 10px 8px 7px;
		width: 108px;
		background: rgba(15, 23, 42, 0.85);
		border: 1px solid rgba(255, 255, 255, 0.07);
		border-top: 2px solid var(--rc);
		border-radius: 8px;
		text-decoration: none;
		user-select: none;
		transition: transform 0.15s, box-shadow 0.15s;
	}

	.desk.is-live:hover {
		transform: translateY(-3px);
		box-shadow: 0 6px 24px color-mix(in srgb, var(--rc) 25%, transparent);
		border-color: var(--rc);
		cursor: pointer;
	}

	.desk.is-offline {
		opacity: 0.45;
		filter: grayscale(50%);
	}

	/* ── Status animations ── */
	.status-coding  { animation: bounce 0.55s ease-in-out infinite alternate; }
	.status-thinking{ animation: float  2s    ease-in-out infinite alternate; }
	.status-stuck   { animation: shake  0.35s ease-in-out infinite; border-top-color: #ef4444 !important; }
	.status-waiting { animation: pulse-ring 2s ease-in-out infinite; }
	.status-done    { box-shadow: 0 0 18px rgba(74, 222, 128, 0.25); }

	@keyframes bounce {
		from { transform: translateY(0); }
		to   { transform: translateY(-4px); }
	}
	@keyframes float {
		from { transform: translateY(0); }
		to   { transform: translateY(-5px); }
	}
	@keyframes shake {
		0%, 100% { transform: translateX(0); }
		33%       { transform: translateX(-3px); }
		66%       { transform: translateX(3px); }
	}
	@keyframes pulse-ring {
		0%, 100% { box-shadow: 0 0 0 2px rgba(249, 115, 22, 0.25); }
		50%       { box-shadow: 0 0 0 7px rgba(249, 115, 22, 0.07); }
	}

	/* ── Badge ── */
	.badge {
		position: absolute;
		top: -9px;
		right: -4px;
		font-size: 13px;
		line-height: 1;
		filter: drop-shadow(0 1px 3px rgba(0,0,0,0.5));
	}

	/* ── Character ── */
	.char { margin-bottom: 1px; }
	.char-emoji { font-size: 22px; line-height: 1; display: block; }

	/* ── Monitor ── */
	.monitor {
		width: 82px;
		height: 46px;
		background: var(--mbg);
		border: 1px solid rgba(255, 255, 255, 0.08);
		border-radius: 3px;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 5px;
		overflow: hidden;
	}

	/* Coding lines */
	.code-lines { display: flex; flex-direction: column; gap: 4px; width: 100%; }
	.code-line {
		height: 3px;
		background: var(--mfg);
		border-radius: 2px;
		transform-origin: left;
		animation: type-in 1.1s ease-in-out infinite;
	}
	.l1 { width: 72%; animation-delay: 0s; }
	.l2 { width: 48%; animation-delay: 0.18s; }
	.l3 { width: 62%; animation-delay: 0.36s; }
	@keyframes type-in {
		0%, 100% { transform: scaleX(0.5); opacity: 0.4; }
		50%       { transform: scaleX(1);   opacity: 1;   }
	}

	/* Thinking orb */
	.think-orb {
		width: 10px; height: 10px;
		background: var(--mfg);
		border-radius: 50%;
		animation: orb-pulse 1.1s ease-in-out infinite;
	}
	@keyframes orb-pulse {
		0%, 100% { transform: scale(0.7); opacity: 0.5; box-shadow: 0 0 0 0 var(--mfg); }
		50%       { transform: scale(1.3); opacity: 1;   box-shadow: 0 0 8px 2px color-mix(in srgb, var(--mfg) 40%, transparent); }
	}

	/* Stuck bang */
	.stuck-bang {
		font-size: 20px; font-weight: 900; color: #ef4444;
		animation: blink 0.7s step-end infinite;
	}
	@keyframes blink { 0%, 100% { opacity: 1; } 50% { opacity: 0; } }

	/* Done check */
	.done-mark { font-size: 18px; color: #4ade80; }

	/* Waiting bar */
	.wait-bar {
		width: 50px; height: 4px;
		background: var(--mfg);
		border-radius: 2px;
		animation: wait-slide 1.8s ease-in-out infinite;
	}
	@keyframes wait-slide {
		0%, 100% { transform: scaleX(0.3) translateX(-30px); opacity: 0.4; }
		50%       { transform: scaleX(1)   translateX(0);     opacity: 1; }
	}

	/* ── Label ── */
	.label { display: flex; flex-direction: column; align-items: center; gap: 1px; width: 100%; }
	.name {
		font-size: 11px; font-weight: 600; color: #e2e8f0;
		white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 92px;
	}
	.status-text {
		font-size: 9px; font-weight: 500;
		color: var(--mfg);
		opacity: 0.9;
	}

	/* ── Task chip ── */
	.task-chip {
		font-size: 8px; color: rgba(226, 232, 240, 0.5);
		background: rgba(255, 255, 255, 0.04);
		border-radius: 3px; padding: 2px 5px;
		max-width: 92px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
		line-height: 1.4; text-align: center;
	}
</style>
