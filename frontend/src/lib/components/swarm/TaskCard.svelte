<script lang="ts">
	const STAGES = ['spec', 'implement', 'test', 'deploy', 'done'] as const;
	type Stage = (typeof STAGES)[number];

	interface Agent {
		id: string;
		name: string;
		role: string;
	}

	interface Task {
		id: string;
		title: string;
		description?: string | null;
		stage: Stage;
		agent_id?: string | null;
		project?: string | null;
		pr_url?: string | null;
	}

	interface Props {
		task: Task;
		agents: Agent[];
		onMoveStage: (taskId: string, stage: Stage) => void;
		onDelete: (taskId: string) => void;
		onAssign: (taskId: string, agentId: string | null) => void;
		onInjectBrief?: (task: Task) => void;
		onCreatePR?: (taskId: string) => Promise<void>;
	}

	let { task, agents, onMoveStage, onDelete, onAssign, onInjectBrief, onCreatePR }: Props = $props();
	let creatingPR = $state(false);

	async function handleCreatePR() {
		if (!onCreatePR) return;
		creatingPR = true;
		try {
			await onCreatePR(task.id);
		} finally {
			creatingPR = false;
		}
	}

	const stageColors: Record<string, string> = {
		spec: 'bg-blue-100 text-blue-700',
		implement: 'bg-vanna-teal/15 text-vanna-teal',
		test: 'bg-yellow-100 text-yellow-700',
		deploy: 'bg-orange-100 text-orange-700',
		done: 'bg-green-100 text-green-700'
	};

	let currentStageIdx = $derived(STAGES.indexOf(task.stage as Stage));
	let canMoveLeft = $derived(currentStageIdx > 0);
	let canMoveRight = $derived(currentStageIdx < STAGES.length - 1);
	let assignedAgent = $derived(agents.find((a) => a.id === task.agent_id) ?? null);
	let agentIsLive = $derived(!!(assignedAgent as any)?.tmux_session);

	let showAssignMenu = $state(false);

	function handleMoveLeft() {
		if (canMoveLeft) onMoveStage(task.id, STAGES[currentStageIdx - 1]);
	}

	function handleMoveRight() {
		if (canMoveRight) onMoveStage(task.id, STAGES[currentStageIdx + 1]);
	}

	function handleAssign(agentId: string | null) {
		onAssign(task.id, agentId);
		showAssignMenu = false;
	}
</script>

<div class="bg-white rounded-xl border border-slate-200 shadow-sm p-3 group hover:shadow-md hover:border-vanna-teal/30 transition-all duration-200">
	<!-- Title row -->
	<div class="flex items-start justify-between gap-2 mb-2">
		<p class="text-sm font-medium text-vanna-navy leading-snug flex-1">{task.title}</p>
		<button
			type="button"
			onclick={() => onDelete(task.id)}
			class="opacity-0 group-hover:opacity-100 flex-shrink-0 p-1 text-slate-300 hover:text-red-400 transition-all rounded-lg"
			title="Delete task"
		>
			<svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
				<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
			</svg>
		</button>
	</div>

	<!-- Description -->
	{#if task.description}
		<p class="text-xs text-slate-400 mb-2 line-clamp-2">{task.description}</p>
	{/if}

	<!-- Project tag -->
	{#if task.project}
		<div class="mb-2">
			<span class="text-xs text-slate-400 bg-slate-100 rounded-full px-2 py-0.5">{task.project}</span>
		</div>
	{/if}

	<!-- Agent assignment -->
	<div class="relative mb-2">
		<button
			type="button"
			onclick={() => (showAssignMenu = !showAssignMenu)}
			class="w-full text-left text-xs px-2 py-1 rounded-lg border border-dashed
				{assignedAgent
				? 'border-vanna-teal/40 bg-vanna-teal/5 text-vanna-teal'
				: 'border-slate-200 text-slate-400 hover:border-vanna-teal/30 hover:text-vanna-teal/70'}
				transition-colors"
		>
			{assignedAgent ? `→ ${assignedAgent.name}` : 'Assign agent…'}
		</button>

		{#if showAssignMenu}
			<div
				class="absolute top-full left-0 mt-1 w-full bg-white rounded-xl border border-slate-200 shadow-lg z-10 overflow-hidden"
			>
				<button
					type="button"
					onclick={() => handleAssign(null)}
					class="w-full text-left text-xs px-3 py-2 text-slate-400 hover:bg-vanna-cream/50 transition-colors"
				>
					Unassign
				</button>
				{#each agents as agent}
					<button
						type="button"
						onclick={() => handleAssign(agent.id)}
						class="w-full text-left text-xs px-3 py-2 text-vanna-navy hover:bg-vanna-cream/50 transition-colors
							{task.agent_id === agent.id ? 'font-semibold text-vanna-teal' : ''}"
					>
						{agent.name}
					</button>
				{/each}
			</div>
		{/if}
	</div>

	<!-- PR badge -->
	{#if task.pr_url}
		<a
			href={task.pr_url}
			target="_blank"
			rel="noopener noreferrer"
			class="flex items-center gap-1 text-xs text-purple-600 bg-purple-50 border border-purple-200 rounded-lg px-2 py-1 mb-2 hover:bg-purple-100 transition-colors truncate"
			title={task.pr_url}
		>
			<svg class="w-3 h-3 flex-shrink-0" fill="currentColor" viewBox="0 0 16 16">
				<path d="M1.5 3.25a2.25 2.25 0 1 1 3 2.122v5.256a2.251 2.251 0 1 1-1.5 0V5.372A2.25 2.25 0 0 1 1.5 3.25Zm5.677-.177L9.573.677A.25.25 0 0 1 10 .854V2.5h1A2.5 2.5 0 0 1 13.5 5v5.628a2.251 2.251 0 1 1-1.5 0V5a1 1 0 0 0-1-1h-1v1.646a.25.25 0 0 1-.427.177L7.177 3.427a.25.25 0 0 1 0-.354Z"/>
			</svg>
			View PR
		</a>
	{:else if task.stage === 'deploy' && onCreatePR}
		<button
			type="button"
			onclick={handleCreatePR}
			disabled={creatingPR}
			class="w-full text-xs py-1 rounded-lg bg-purple-50 text-purple-600 border border-purple-200 font-medium hover:bg-purple-100 disabled:opacity-50 transition-colors mb-2"
			title="Create GitHub pull request"
		>
			{creatingPR ? 'Creating PR…' : '↗ Create PR'}
		</button>
	{/if}

	<!-- Inject brief button (only when agent is live) -->
	{#if agentIsLive && onInjectBrief}
		<button
			type="button"
			onclick={() => onInjectBrief!(task)}
			class="w-full text-xs py-1 rounded-lg bg-vanna-teal/10 text-vanna-teal font-medium hover:bg-vanna-teal/20 transition-colors mb-2"
			title="Send task brief to agent"
		>
			⚡ Inject brief
		</button>
	{/if}

	<!-- Stage navigation -->
	<div class="flex items-center justify-between mt-1">
		<button
			type="button"
			onclick={handleMoveLeft}
			disabled={!canMoveLeft}
			class="p-1 rounded-lg text-slate-300 hover:text-vanna-teal hover:bg-vanna-teal/10 disabled:opacity-0 transition-all"
			title="Move to previous stage"
		>
			<svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
				<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 19l-7-7 7-7" />
			</svg>
		</button>

		<span class="text-xs px-2 py-0.5 rounded-full font-medium {stageColors[task.stage] ?? ''}">
			{task.stage}
		</span>

		<button
			type="button"
			onclick={handleMoveRight}
			disabled={!canMoveRight}
			class="p-1 rounded-lg text-slate-300 hover:text-vanna-teal hover:bg-vanna-teal/10 disabled:opacity-0 transition-all"
			title="Move to next stage"
		>
			<svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
				<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 5l7 7-7 7" />
			</svg>
		</button>
	</div>
</div>
