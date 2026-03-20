<script lang="ts">
	interface Agent {
		id: string;
		name: string;
		role: string;
		status: string;
		tmux_session?: string | null;
		worktree_path?: string | null;
		repo_path?: string | null;
		current_file?: string | null;
		current_task_id?: string | null;
		project?: string | null;
		latest_note?: string | null;
	}

	interface Task {
		id: string;
		title: string;
		stage: string;
	}

	interface Props {
		agent: Agent;
		tasks: Task[];
		sessionId: string;
		onSpawn: (agentId: string) => void;
		onDespawn: (agentId: string) => void;
		onAddNote?: (agentId: string, content: string) => Promise<void>;
	}

	let { agent, tasks, sessionId, onSpawn, onDespawn, onAddNote }: Props = $props();

	let showNoteForm = $state(false);
	let noteText = $state('');
	let savingNote = $state(false);

	let showConfigForm = $state(false);
	let repoPathInput = $state(agent.repo_path ?? '');
	let savingConfig = $state(false);
	// Local display copy — updated optimistically on save; parent polling will sync later
	let localRepoPath = $state<string | null>(agent.repo_path ?? null);

	async function saveConfig() {
		savingConfig = true;
		const newPath = repoPathInput.trim() || null;
		try {
			const res = await fetch(`/api/swarm/sessions/${sessionId}/agents/${agent.id}`, {
				method: 'PATCH',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ repo_path: newPath })
			});
			if (res.ok) {
				localRepoPath = newPath;
				showConfigForm = false;
			}
		} finally {
			savingConfig = false;
		}
	}

	async function saveNote() {
		const text = noteText.trim();
		if (!text || !onAddNote) return;
		savingNote = true;
		try {
			await onAddNote(agent.id, text);
			noteText = '';
			showNoteForm = false;
		} finally {
			savingNote = false;
		}
	}

	const roleLabels: Record<string, string> = {
		orchestrator: 'Orchestrator',
		'senior-dev': 'Senior Dev',
		'qa-agent': 'QA Agent',
		'devops-agent': 'DevOps',
		researcher: 'Researcher',
		worker: 'Worker'
	};

	const roleColors: Record<string, string> = {
		orchestrator: 'bg-vanna-magenta/20 text-vanna-magenta border-vanna-magenta/30',
		'senior-dev': 'bg-vanna-teal/20 text-vanna-teal border-vanna-teal/30',
		'qa-agent': 'bg-yellow-100 text-yellow-700 border-yellow-200',
		'devops-agent': 'bg-blue-100 text-blue-700 border-blue-200',
		researcher: 'bg-purple-100 text-purple-700 border-purple-200',
		worker: 'bg-slate-100 text-slate-600 border-slate-200'
	};

	const statusConfig: Record<string, { dot: string; label: string; pulse: boolean; ring: string }> = {
		idle:     { dot: 'bg-slate-300',    label: 'Idle',      pulse: false, ring: '' },
		thinking: { dot: 'bg-blue-400',     label: 'Thinking…', pulse: true,  ring: 'ring-1 ring-blue-200' },
		coding:   { dot: 'bg-vanna-teal',   label: 'Coding',    pulse: true,  ring: 'ring-1 ring-vanna-teal/30' },
		testing:  { dot: 'bg-yellow-400',   label: 'Testing',   pulse: true,  ring: 'ring-1 ring-yellow-200' },
		waiting:  { dot: 'bg-orange-400',   label: 'Waiting',   pulse: false, ring: 'ring-1 ring-orange-200' },
		stuck:    { dot: 'bg-red-500',      label: 'Stuck',     pulse: false, ring: 'ring-1 ring-red-200' },
		done:     { dot: 'bg-green-500',    label: 'Done',      pulse: false, ring: '' }
	};

	let statusInfo = $derived(statusConfig[agent.status] ?? statusConfig.idle);
	let currentTask = $derived(tasks.find((t) => t.id === agent.current_task_id) ?? null);
	let roleLabel = $derived(roleLabels[agent.role] ?? agent.role);
	let roleColor = $derived(roleColors[agent.role] ?? roleColors.worker);
	let isLive = $derived(!!agent.tmux_session);
	let canSpawn = $derived(!isLive && !!localRepoPath);
	let terminalHref = $derived(isLive ? `/terminal/${agent.tmux_session}` : null);

	function initials(name: string) {
		return name.split(' ').map((w) => w[0]).join('').slice(0, 2).toUpperCase();
	}
</script>

<div
	class="bg-white/80 rounded-2xl border border-slate-200 shadow-sm p-4 transition-all duration-200
		{isLive ? 'border-vanna-teal/40 shadow-md ' + statusInfo.ring : 'hover:shadow-md hover:border-vanna-teal/30'}"
>
	<!-- Avatar row -->
	<div class="flex items-start gap-3 mb-3">
		<div class="relative flex-shrink-0">
			<div
				class="w-11 h-11 rounded-xl flex items-center justify-center font-bold text-sm
					{agent.role === 'orchestrator' ? 'bg-vanna-magenta/20 text-vanna-magenta' : 'bg-vanna-teal/15 text-vanna-teal'}"
			>
				{initials(agent.name)}
			</div>
			<span
				class="absolute -bottom-0.5 -right-0.5 w-3 h-3 rounded-full border-2 border-white {statusInfo.dot}
					{statusInfo.pulse ? 'animate-pulse' : ''}"
			></span>
		</div>

		<div class="flex-1 min-w-0">
			<p class="font-semibold text-sm text-vanna-navy truncate">{agent.name}</p>
			<span class="inline-block text-xs px-1.5 py-0.5 rounded-md border font-medium {roleColor}">
				{roleLabel}
			</span>
		</div>

		<!-- Live indicator -->
		{#if isLive}
			<div class="flex-shrink-0 flex items-center gap-1">
				<span class="w-1.5 h-1.5 rounded-full bg-vanna-teal animate-pulse"></span>
				<span class="text-xs text-vanna-teal font-medium">Live</span>
			</div>
		{/if}
	</div>

	<!-- Status -->
	<div class="text-xs font-medium mb-2 {isLive ? 'text-vanna-teal' : 'text-slate-400'}">
		{statusInfo.label}
	</div>

	<!-- Latest note -->
	{#if agent.latest_note}
		<div class="text-xs text-slate-500 bg-amber-50 border border-amber-100 rounded-lg px-2 py-1 mb-2 leading-snug line-clamp-2">
			<span class="text-amber-400 font-medium">Note: </span>{agent.latest_note}
		</div>
	{/if}

	<!-- Current task / file -->
	{#if currentTask}
		<div class="text-xs text-slate-500 truncate bg-vanna-cream/50 rounded-lg px-2 py-1 mb-2">
			<span class="text-slate-400">Task:</span> {currentTask.title}
		</div>
	{:else if agent.current_file}
		<div class="text-xs text-slate-500 truncate bg-vanna-cream/50 rounded-lg px-2 py-1 font-mono mb-2">
			{agent.current_file.split('/').slice(-2).join('/')}
		</div>
	{/if}

	<!-- Repo path / configure -->
	{#if !isLive}
		{#if showConfigForm}
			<div class="mb-2">
				<input
					type="text"
					bind:value={repoPathInput}
					placeholder="/path/to/repo"
					class="w-full px-2 py-1 text-xs border border-slate-200 rounded-lg font-mono focus:outline-none focus:ring-1 focus:ring-vanna-teal"
				/>
				<div class="flex gap-1 mt-1">
					<button
						type="button"
						onclick={saveConfig}
						disabled={savingConfig}
						class="flex-1 text-xs py-1 rounded-lg bg-vanna-teal text-white font-medium hover:bg-vanna-teal/90 disabled:opacity-50 transition-colors"
					>
						{savingConfig ? 'Saving…' : 'Save'}
					</button>
					<button
						type="button"
						onclick={() => { showConfigForm = false; repoPathInput = agent.repo_path ?? ''; }}
						class="text-xs px-2 py-1 rounded-lg border border-slate-200 text-slate-400 hover:bg-slate-50 transition-colors"
					>
						Cancel
					</button>
				</div>
			</div>
		{:else if localRepoPath}
			<button
				type="button"
				onclick={() => { repoPathInput = localRepoPath ?? ''; showConfigForm = true; }}
				class="text-xs text-slate-300 font-mono truncate mb-2 w-full text-left hover:text-vanna-teal transition-colors"
				title="Click to change repo path"
			>
				{localRepoPath.split('/').slice(-2).join('/')}
			</button>
		{:else}
			<button
				type="button"
				onclick={() => { repoPathInput = ''; showConfigForm = true; }}
				class="flex items-center gap-1 text-xs text-slate-400 italic mb-2 hover:text-vanna-teal transition-colors"
				title="Set repo path"
			>
				<svg class="w-3 h-3 flex-shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.065 2.572c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.572 1.065c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.065-2.572c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z M15 12a3 3 0 11-6 0 3 3 0 016 0z"/>
				</svg>
				Configure repo path
			</button>
		{/if}
	{/if}

	<!-- Action buttons -->
	<div class="flex items-center gap-1.5 mt-2">
		{#if isLive}
			<!-- Terminal link -->
			{#if terminalHref}
				<a
					href={terminalHref}
					class="flex-1 flex items-center justify-center gap-1 py-1.5 rounded-lg bg-vanna-teal/10 text-vanna-teal text-xs font-medium hover:bg-vanna-teal/20 transition-colors"
					title="Open terminal"
				>
					<svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"/>
					</svg>
					Terminal
				</a>
			{/if}
			<!-- Despawn -->
			<button
				type="button"
				onclick={() => onDespawn(agent.id)}
				class="flex items-center justify-center gap-1 px-2 py-1.5 rounded-lg border border-red-200 text-red-400 text-xs hover:bg-red-50 transition-colors"
				title="Stop agent"
			>
				<svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 12a9 9 0 11-18 0 9 9 0 0118 0z M9 10a1 1 0 011-1h4a1 1 0 011 1v4a1 1 0 01-1 1h-4a1 1 0 01-1-1v-4z"/>
				</svg>
				Stop
			</button>
		{:else if canSpawn}
			<!-- Spawn -->
			<button
				type="button"
				onclick={() => onSpawn(agent.id)}
				class="flex-1 flex items-center justify-center gap-1 py-1.5 rounded-lg bg-vanna-teal text-white text-xs font-medium hover:bg-vanna-teal/90 transition-colors"
				title="Spawn agent"
			>
				<svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M14.752 11.168l-3.197-2.132A1 1 0 0010 9.87v4.263a1 1 0 001.555.832l3.197-2.132a1 1 0 000-1.664z M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/>
				</svg>
				Spawn
			</button>
		{:else}
			<button
				type="button"
				onclick={() => { repoPathInput = ''; showConfigForm = true; }}
				class="flex-1 flex items-center justify-center gap-1 py-1.5 rounded-lg border border-dashed border-slate-200 text-slate-400 text-xs hover:border-vanna-teal/40 hover:text-vanna-teal transition-colors"
			>
				Set repo path to spawn
			</button>
		{/if}
	</div>

	<!-- Project tag -->
	{#if agent.project}
		<div class="mt-2">
			<span class="text-xs text-slate-400 bg-slate-100 rounded-full px-2 py-0.5">{agent.project}</span>
		</div>
	{/if}

	<!-- Add note -->
	{#if onAddNote}
		{#if showNoteForm}
			<div class="mt-2">
				<textarea
					bind:value={noteText}
					placeholder="Add a note for this agent…"
					rows={2}
					class="w-full px-2 py-1.5 text-xs border border-amber-200 rounded-lg resize-none focus:outline-none focus:ring-1 focus:ring-amber-300 bg-amber-50/50"
				></textarea>
				<div class="flex gap-1 mt-1">
					<button
						type="button"
						onclick={saveNote}
						disabled={savingNote || !noteText.trim()}
						class="flex-1 text-xs py-1 rounded-lg bg-amber-400 text-white font-medium hover:bg-amber-500 disabled:opacity-50 transition-colors"
					>
						{savingNote ? 'Saving…' : 'Save note'}
					</button>
					<button
						type="button"
						onclick={() => { showNoteForm = false; noteText = ''; }}
						class="text-xs px-2 py-1 rounded-lg border border-slate-200 text-slate-400 hover:bg-slate-50 transition-colors"
					>
						Cancel
					</button>
				</div>
			</div>
		{:else}
			<button
				type="button"
				onclick={() => (showNoteForm = true)}
				class="mt-2 w-full text-xs py-1 rounded-lg border border-dashed border-amber-200 text-amber-400 hover:border-amber-300 hover:text-amber-500 transition-colors"
			>
				+ Add note
			</button>
		{/if}
	{/if}
</div>
