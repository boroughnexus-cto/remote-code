<script lang="ts">
	import AgentTerminal from './AgentTerminal.svelte';

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
		model_name?: string | null;
		allowed_tools?: string | null;
		disallowed_tools?: string | null;
		dangerously_skip_permissions?: boolean;
		capabilities?: string[] | null;
		context_pct?: number | null;
		context_state?: string | null;
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
		spawning?: boolean;
		onSpawn: (agentId: string) => void;
		onDespawn: (agentId: string) => void;
		onAddNote?: (agentId: string, content: string) => Promise<void>;
	}

	let { agent, tasks, sessionId, spawning = false, onSpawn, onDespawn, onAddNote }: Props = $props();

	let showNoteForm = $state(false);
	let noteText = $state('');
	let savingNote = $state(false);

	let showConfigForm = $state(false);
	let repoPathInput = $state(agent.repo_path ?? '');
	let modelNameInput = $state(agent.model_name ?? '');
	let allowedToolsInput = $state(agent.allowed_tools ?? '');
	let disallowedToolsInput = $state(agent.disallowed_tools ?? '');
	let dangerouslySkipInput = $state(agent.dangerously_skip_permissions ?? true);
	let capabilitiesInput = $state((agent.capabilities ?? []).join(', '));
	let savingConfig = $state(false);
	// Local display copies — updated optimistically on save; parent polling will sync later
	let localRepoPath = $state<string | null>(agent.repo_path ?? null);
	let localModelName = $state<string | null>(agent.model_name ?? null);
	let localAllowedTools = $state<string | null>(agent.allowed_tools ?? null);
	let localDisallowedTools = $state<string | null>(agent.disallowed_tools ?? null);
	let localDangerouslySkip = $state(agent.dangerously_skip_permissions ?? true);
	let localCapabilities = $state<string[]>(agent.capabilities ?? []);

	let showTerminal = $state(false);

	async function saveConfig() {
		savingConfig = true;
		const newPath = repoPathInput.trim() || null;
		const newModel = modelNameInput.trim() || null;
		const newAllowed = allowedToolsInput.trim() || null;
		const newDisallowed = disallowedToolsInput.trim() || null;
		try {
			const res = await fetch(`/api/swarm/sessions/${sessionId}/agents/${agent.id}`, {
				method: 'PATCH',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ repo_path: newPath, model_name: newModel, allowed_tools: newAllowed, disallowed_tools: newDisallowed, dangerously_skip_permissions: dangerouslySkipInput, capabilities: capabilitiesInput.trim() || null })
			});
			if (res.ok) {
				localRepoPath = newPath;
				localModelName = newModel;
				localAllowedTools = newAllowed;
				localDisallowedTools = newDisallowed;
				localDangerouslySkip = dangerouslySkipInput;
				showConfigForm = false;
				localCapabilities = capabilitiesInput.trim() ? capabilitiesInput.split(",").map(c => c.trim().toLowerCase()).filter(Boolean) : [];
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
	let contextPct = $derived(Math.min(1, Math.max(0, agent.context_pct ?? 0)));
	let contextState = $derived(agent.context_state ?? '');
	let showContextBar = $derived(isLive && contextPct > 0.5);

	function initials(name: string) {
		return name.split(' ').map((w) => w[0]).join('').slice(0, 2).toUpperCase();
	}

	async function sendCompact() {
		await fetch(`/api/swarm/sessions/${sessionId}/agents/${agent.id}/inject`, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ text: '/compact' })
		});
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
		{#if contextState === 'overflow'}
			<span class="ml-1 text-red-500 font-semibold">· Context full</span>
		{:else if contextState === 'critical'}
			<span class="ml-1 text-orange-500 font-semibold">· Context critical</span>
		{:else if contextState === 'high'}
			<span class="ml-1 text-yellow-600">· Context high</span>
		{/if}
	</div>

	<!-- Context overflow bar -->
	{#if showContextBar}
		{@const pct = Math.round(contextPct * 100)}
		{@const barColor = contextPct >= 0.95 ? 'bg-red-500' : contextPct >= 0.85 ? 'bg-orange-400' : 'bg-yellow-400'}
		<div class="mb-2">
			<div class="flex items-center justify-between mb-0.5">
				<span class="text-xs text-slate-400">Context</span>
				<span class="text-xs font-medium {contextPct >= 0.95 ? 'text-red-500' : contextPct >= 0.85 ? 'text-orange-500' : 'text-yellow-600'}">{pct}%</span>
			</div>
			<div class="h-1.5 bg-slate-100 rounded-full overflow-hidden">
				<div class="h-full {barColor} rounded-full transition-all duration-500" style="width:{pct}%"></div>
			</div>
		</div>
	{/if}

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
			<div class="mb-2 space-y-1">
				<input
					type="text"
					bind:value={repoPathInput}
					placeholder="/path/to/repo"
					class="w-full px-2 py-1 text-xs border border-slate-200 rounded-lg font-mono focus:outline-none focus:ring-1 focus:ring-vanna-teal"
				/>
				<input
					type="text"
					bind:value={modelNameInput}
					placeholder="Model (e.g. claude-sonnet-4-5)"
					class="w-full px-2 py-1 text-xs border border-slate-200 rounded-lg font-mono focus:outline-none focus:ring-1 focus:ring-vanna-teal"
				/>
				<input
					type="text"
					bind:value={allowedToolsInput}
					placeholder="Allowed tools (e.g. Read,Write,Bash)"
					class="w-full px-2 py-1 text-xs border border-slate-200 rounded-lg font-mono focus:outline-none focus:ring-1 focus:ring-vanna-teal"
					title="Comma-separated tool names to allow. Leave blank for no restriction."
				/>
				<input
					type="text"
					bind:value={disallowedToolsInput}
					placeholder="Disallowed tools (e.g. Bash)"
					class="w-full px-2 py-1 text-xs border border-slate-200 rounded-lg font-mono focus:outline-none focus:ring-1 focus:ring-vanna-teal"
					title="Comma-separated tool names to disallow. Leave blank for no restriction."
				/>
				<label class="flex items-center gap-2 px-1 py-0.5 rounded-lg border {dangerouslySkipInput ? 'border-red-200 bg-red-50/50' : 'border-slate-200'} cursor-pointer select-none">
					<input
						type="checkbox"
						bind:checked={dangerouslySkipInput}
						class="rounded text-red-500 focus:ring-red-300"
					/>
					<span class="text-xs {dangerouslySkipInput ? 'text-red-600 font-medium' : 'text-slate-500'}">
						{dangerouslySkipInput ? '⚠ Skip permission checks' : 'Enforce permission checks'}
					</span>
				</label>
				<input
					type="text"
					bind:value={capabilitiesInput}
					placeholder="Capabilities (e.g. python, docker, testing)"
					class="w-full px-2 py-1 text-xs border border-slate-200 rounded-lg focus:outline-none focus:ring-1 focus:ring-vanna-teal"
					title="Comma-separated capabilities this agent has. Used for smart task routing."
				/>
				<div class="flex gap-1">
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
						onclick={() => { showConfigForm = false; repoPathInput = localRepoPath ?? ''; modelNameInput = localModelName ?? ''; allowedToolsInput = localAllowedTools ?? ''; disallowedToolsInput = localDisallowedTools ?? ''; dangerouslySkipInput = localDangerouslySkip; capabilitiesInput = localCapabilities.join(', '); }}
						class="text-xs px-2 py-1 rounded-lg border border-slate-200 text-slate-400 hover:bg-slate-50 transition-colors"
					>
						Cancel
					</button>
				</div>
			</div>
		{:else if localRepoPath}
			<button
				type="button"
				onclick={() => { repoPathInput = localRepoPath ?? ''; modelNameInput = localModelName ?? ''; allowedToolsInput = localAllowedTools ?? ''; disallowedToolsInput = localDisallowedTools ?? ''; dangerouslySkipInput = localDangerouslySkip; capabilitiesInput = localCapabilities.join(', '); showConfigForm = true; }}
				class="text-xs text-slate-300 font-mono truncate mb-2 w-full text-left hover:text-vanna-teal transition-colors"
				title="Click to configure"
			>
				{localRepoPath.split('/').slice(-2).join('/')}{localModelName ? ` · ${localModelName}` : ''}
			</button>
		{:else}
			<button
				type="button"
				onclick={() => { repoPathInput = ''; modelNameInput = localModelName ?? ''; allowedToolsInput = localAllowedTools ?? ''; disallowedToolsInput = localDisallowedTools ?? ''; dangerouslySkipInput = localDangerouslySkip; capabilitiesInput = localCapabilities.join(', '); showConfigForm = true; }}
				class="flex items-center gap-1 text-xs text-slate-400 italic mb-2 hover:text-vanna-teal transition-colors"
				title="Configure agent"
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
			<!-- Inline terminal toggle -->
			<button
				type="button"
				aria-label="{showTerminal ? 'Hide terminal' : 'Show inline terminal'}"
				onclick={() => (showTerminal = !showTerminal)}
				class="flex items-center justify-center gap-1 px-2 py-1.5 rounded-lg text-xs transition-colors
					{showTerminal ? 'bg-vanna-teal text-white' : 'bg-vanna-teal/10 text-vanna-teal hover:bg-vanna-teal/20'}"
				title="{showTerminal ? 'Hide terminal' : 'Show inline terminal'}"
			>
				<svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"/>
				</svg>
			</button>
			<!-- Full terminal link -->
			{#if terminalHref}
				<a
					href={terminalHref}
					aria-label="Open full terminal"
					class="flex items-center justify-center gap-1 px-2 py-1.5 rounded-lg bg-vanna-teal/10 text-vanna-teal text-xs font-medium hover:bg-vanna-teal/20 transition-colors"
					title="Open full terminal"
				>
					<svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M10 6H6a2 2 0 00-2 2v10a2 2 0 002 2h10a2 2 0 002-2v-4M14 4h6m0 0v6m0-6L10 14"/>
					</svg>
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
				disabled={spawning}
				class="flex-1 flex items-center justify-center gap-1.5 py-2 rounded-lg text-sm font-semibold transition-all
					{spawning
						? 'bg-vanna-teal/60 text-white/70 cursor-not-allowed'
						: 'bg-gradient-to-r from-vanna-teal to-teal-500 text-white shadow-md shadow-vanna-teal/30 hover:shadow-lg hover:shadow-vanna-teal/40 hover:brightness-110 active:scale-95'}"
				title="Spawn agent"
			>
				{#if spawning}
					<svg class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24">
						<circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="3"/>
						<path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8v8H4z"/>
					</svg>
					Spawning…
				{:else}
					<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"/>
					</svg>
					Spawn
				{/if}
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

	<!-- Capabilities pills -->
	{#if localCapabilities.length > 0}
		<div class="mt-2 flex flex-wrap gap-1">
			{#each localCapabilities as cap}
				<span class="text-xs bg-vanna-teal/10 text-vanna-teal border border-vanna-teal/20 rounded-full px-1.5 py-0.5">{cap}</span>
			{/each}
		</div>
	{/if}

	<!-- Context compact button -->
	{#if showContextBar && contextState !== 'normal'}
		<div class="mt-2">
			<button
				type="button"
				onclick={sendCompact}
				class="w-full text-xs py-1 rounded-lg border border-dashed
					{contextPct >= 0.95 ? 'border-red-300 text-red-500 hover:bg-red-50' : 'border-orange-200 text-orange-500 hover:bg-orange-50'}
					transition-colors"
				title="Send /compact to reduce context usage"
			>
				Compact context
			</button>
		</div>
	{/if}

	<!-- Inline terminal -->
	{#if showTerminal && isLive && agent.tmux_session}
		<div class="mt-3">
			<AgentTerminal tmuxSession={agent.tmux_session} />
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
