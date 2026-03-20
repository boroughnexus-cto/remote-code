<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import AgentAvatar from '$lib/components/swarm/AgentAvatar.svelte';
	import TaskCard from '$lib/components/swarm/TaskCard.svelte';
	import OrchestratorPanel from '$lib/components/swarm/OrchestratorPanel.svelte';
	import EventFeed from '$lib/components/swarm/EventFeed.svelte';
	import VoiceConversation from '$lib/components/VoiceConversation.svelte';
	import { STAGES, type Stage, STAGE_LABELS, STAGE_COLORS } from '$lib/workflowStages';

	const ROLES = [
		{ value: 'orchestrator', label: 'Orchestrator' },
		{ value: 'senior-dev', label: 'Senior Dev' },
		{ value: 'qa-agent', label: 'QA Agent' },
		{ value: 'devops-agent', label: 'DevOps' },
		{ value: 'researcher', label: 'Researcher' },
		{ value: 'worker', label: 'Worker' }
	];

	interface Agent {
		id: string;
		session_id: string;
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
		session_id: string;
		title: string;
		description?: string | null;
		stage: Stage;
		agent_id?: string | null;
		project?: string | null;
		pr_url?: string | null;
	}

	interface SwarmEvent {
		id: number;
		session_id: string;
		agent_id?: string;
		task_id?: string;
		type: string;
		payload?: string;
		ts: number;
	}

	interface Session {
		id: string;
		name: string;
		created_at: number;
		updated_at: number;
	}

	let sessionId = $derived($page.params.session);

	let session = $state<Session | null>(null);
	let agents = $state<Agent[]>([]);
	let tasks = $state<Task[]>([]);
	let events = $state<SwarmEvent[]>([]);
	let loading = $state(true);
	let error = $state('');
	let ws: WebSocket | null = null;
	let resuming = $state(false);

	// Add agent form
	let showAgentForm = $state(false);
	let newAgentName = $state('');
	let newAgentRole = $state('senior-dev');
	let newAgentProject = $state('');
	let newAgentRepoPath = $state('');
	let addingAgent = $state(false);
	let spawningAgentId = $state<string | null>(null);
	let injectingTaskId = $state<string | null>(null);

	let voiceOpen = $state(false);

	// Add task form — per-column
	let showTaskForm = $state<Record<string, boolean>>({});
	let newTaskTitle = $state('');
	let newTaskDesc = $state('');
	let newTaskProject = $state('');
	let addingTask = $state(false);

	onMount(() => {
		loadState();
		connectWS();
		return () => ws?.close();
	});

	function connectWS() {
		const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
		ws = new WebSocket(`${proto}//${window.location.host}/ws/swarm?session=${sessionId}`);
		ws.onmessage = (e) => {
			try {
				const msg = JSON.parse(e.data);
				if (msg.type === 'swarm_state' && msg.state) {
					applyState(msg.state);
				}
			} catch {}
		};
		ws.onclose = () => {
			// Reconnect after 3s if closed unexpectedly
			setTimeout(() => {
				if (session) connectWS();
			}, 3000);
		};
	}

	function applyState(state: { session: Session; agents: Agent[]; tasks: Task[]; events?: SwarmEvent[] }) {
		session = state.session;
		agents = state.agents;
		tasks = state.tasks;
		events = state.events ?? [];
	}

	async function loadState() {
		try {
			const res = await fetch(`/api/swarm/sessions/${sessionId}`);
			if (res.status === 404) {
				error = 'Session not found';
				return;
			}
			if (res.ok) {
				const state = await res.json();
				applyState(state);
			}
		} catch {
			error = 'Failed to load session';
		} finally {
			loading = false;
		}
	}

	async function addAgent() {
		if (!newAgentName.trim()) return;
		addingAgent = true;
		try {
			await fetch(`/api/swarm/sessions/${sessionId}/agents`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					name: newAgentName.trim(),
					role: newAgentRole,
					project: newAgentProject.trim(),
					repo_path: newAgentRepoPath.trim()
				})
			});
			newAgentName = '';
			newAgentProject = '';
			newAgentRepoPath = '';
			newAgentRole = 'senior-dev';
			showAgentForm = false;
		} finally {
			addingAgent = false;
		}
	}

	async function spawnAgent(agentId: string) {
		spawningAgentId = agentId;
		try {
			const res = await fetch(`/api/swarm/sessions/${sessionId}/agents/${agentId}/spawn`, { method: 'POST' });
			if (!res.ok) {
				const e = await res.json().catch(() => ({ error: 'spawn failed' }));
				alert(`Spawn failed: ${e.error}`);
			}
		} finally {
			spawningAgentId = null;
		}
	}

	async function despawnAgent(agentId: string) {
		if (!confirm('Stop this agent and clean up its worktree?')) return;
		await fetch(`/api/swarm/sessions/${sessionId}/agents/${agentId}/despawn`, { method: 'POST' });
	}

	async function injectTaskBrief(task: Task) {
		const assignedAgent = agents.find((a) => a.id === task.agent_id);
		if (!assignedAgent?.tmux_session) {
			alert('Agent must be spawned and live before injecting a task brief.');
			return;
		}
		const brief = `Task: ${task.title}${task.description ? '\n\nDescription: ' + task.description : ''}${task.project ? '\n\nProject: ' + task.project : ''}\n\nPlease begin working on this task.`;
		injectingTaskId = task.id;
		try {
			const res = await fetch(`/api/swarm/sessions/${sessionId}/agents/${task.agent_id}/inject`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ text: brief })
			});
			if (!res.ok) {
				const e = await res.json().catch(() => ({ error: 'inject failed' }));
				alert(`Inject failed: ${e.error}`);
			}
		} finally {
			injectingTaskId = null;
		}
	}

	async function removeAgent(agentId: string) {
		await fetch(`/api/swarm/sessions/${sessionId}/agents/${agentId}`, { method: 'DELETE' });
	}

	async function addTask(stage: Stage) {
		if (!newTaskTitle.trim()) return;
		addingTask = true;
		try {
			await fetch(`/api/swarm/sessions/${sessionId}/tasks`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({
					title: newTaskTitle.trim(),
					description: newTaskDesc.trim(),
					project: newTaskProject.trim(),
					stage
				})
			});
			newTaskTitle = '';
			newTaskDesc = '';
			newTaskProject = '';
			showTaskForm = {};
		} finally {
			addingTask = false;
		}
	}

	async function moveTask(taskId: string, stage: Stage) {
		await fetch(`/api/swarm/sessions/${sessionId}/tasks/${taskId}`, {
			method: 'PATCH',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ stage })
		});
	}

	async function deleteTask(taskId: string) {
		await fetch(`/api/swarm/sessions/${sessionId}/tasks/${taskId}`, { method: 'DELETE' });
	}

	async function assignTask(taskId: string, agentId: string | null) {
		await fetch(`/api/swarm/sessions/${sessionId}/tasks/${taskId}`, {
			method: 'PATCH',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ agent_id: agentId })
		});
	}

	async function addNote(agentId: string, content: string) {
		const res = await fetch(`/api/swarm/sessions/${sessionId}/agents/${agentId}/note`, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ content, created_by: 'user' })
		});
		if (!res.ok) {
			const e = await res.json().catch(() => ({ error: 'note failed' }));
			throw new Error(e.error);
		}
	}

	async function createPR(taskId: string): Promise<void> {
		const res = await fetch(`/api/swarm/sessions/${sessionId}/tasks/${taskId}/create-pr`, {
			method: 'POST'
		});
		if (!res.ok) {
			const e = await res.json().catch(() => ({ error: 'PR creation failed' }));
			throw new Error(e.error);
		}
	}

	// Derived: live workers (any agent with an active tmux session)
	let liveWorkers = $derived(agents.filter((a) => !!a.tmux_session));

	// Derived: agents that can be resumed (have repo_path but no active session)
	let resumableCount = $derived(
		agents.filter((a) => !!a.repo_path && !a.tmux_session).length
	);

	async function resumeAll() {
		if (!confirm(`Spawn all ${resumableCount} configured-but-idle agent(s)?`)) return;
		resuming = true;
		try {
			const res = await fetch(`/api/swarm/sessions/${sessionId}/resume`, { method: 'POST' });
			if (!res.ok) {
				const e = await res.json().catch(() => ({ error: 'resume failed' }));
				alert(`Resume failed: ${e.error}`);
			}
		} finally {
			resuming = false;
		}
	}

	async function sendToWorkers(text: string, agentId?: string) {
		const body: Record<string, unknown> = { text };
		if (agentId) body.agent_id = agentId;
		const res = await fetch(`/api/swarm/sessions/${sessionId}/message`, {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(body)
		});
		if (!res.ok) {
			const e = await res.json().catch(() => ({ error: 'send failed' }));
			throw new Error(e.error);
		}
	}

	function tasksInStage(stage: Stage) {
		return tasks.filter((t) => t.stage === stage);
	}

	function openTaskForm(stage: Stage) {
		showTaskForm = { [stage]: true };
		newTaskTitle = '';
		newTaskDesc = '';
		newTaskProject = '';
	}
</script>

<svelte:head>
	<title>{session?.name ?? 'Swarm'} — SwarmOps</title>
</svelte:head>

{#if loading}
	<div class="flex items-center justify-center py-20">
		<div class="animate-spin rounded-full h-8 w-8 border-b-2 border-vanna-teal"></div>
	</div>
{:else if error}
	<div class="text-center py-20">
		<p class="text-red-500 font-medium mb-4">{error}</p>
		<button onclick={() => goto('/swarm')} class="text-sm text-vanna-teal hover:underline">← Back to sessions</button>
	</div>
{:else}
	<!-- Header -->
	<div class="flex items-center justify-between mb-6">
		<div class="flex items-center gap-3">
			<button onclick={() => goto('/swarm')} class="p-2 text-slate-400 hover:text-vanna-teal hover:bg-vanna-teal/10 rounded-xl transition-colors">
				<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M15 19l-7-7 7-7" />
				</svg>
			</button>
			<div>
				<h1 class="text-xl font-bold text-vanna-navy">{session?.name}</h1>
				<p class="text-xs text-slate-400">{agents.length} agent{agents.length !== 1 ? 's' : ''} · {tasks.length} task{tasks.length !== 1 ? 's' : ''}</p>
			</div>
		</div>
		<div class="flex items-center gap-2">
			{#if resumableCount > 0}
				<button
					type="button"
					onclick={resumeAll}
					disabled={resuming}
					class="flex items-center gap-1.5 px-3 py-2 rounded-xl font-medium text-sm border border-vanna-teal/40 text-vanna-teal hover:bg-vanna-teal/10 disabled:opacity-50 transition-colors"
				>
					<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
						<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M14.752 11.168l-3.197-2.132A1 1 0 0010 9.87v4.263a1 1 0 001.555.832l3.197-2.132a1 1 0 000-1.664z M21 12a9 9 0 11-18 0 9 9 0 0118 0z"/>
					</svg>
					{resuming ? 'Resuming…' : `Resume ${resumableCount}`}
				</button>
			{/if}
			<!-- Voice button -->
			<button
				type="button"
				onclick={() => (voiceOpen = true)}
				title="Voice assistant"
				class="flex items-center justify-center w-10 h-10 rounded-xl border border-vanna-teal/40 text-vanna-teal hover:bg-vanna-teal/10 transition-colors"
			>
				<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
						d="M19 11a7 7 0 01-7 7m0 0a7 7 0 01-7-7m7 7v4m0 0H8m4 0h4m-4-8a3 3 0 01-3-3V5a3 3 0 116 0v6a3 3 0 01-3 3z" />
				</svg>
			</button>
			<button
				type="button"
				onclick={() => (showAgentForm = !showAgentForm)}
				class="flex items-center gap-2 bg-vanna-teal text-white px-4 py-2 rounded-xl font-medium text-sm hover:bg-vanna-teal/90 transition-colors shadow-vanna-subtle"
			>
				<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
				</svg>
				Add Agent
			</button>
		</div>
	</div>

	<!-- Add agent form -->
	{#if showAgentForm}
		<div class="bg-white/80 rounded-2xl border border-slate-200 shadow-vanna-card p-5 mb-6">
			<h3 class="font-semibold text-vanna-navy mb-4 text-sm">Add Agent to Swarm</h3>
			<form onsubmit={(e) => { e.preventDefault(); addAgent(); }} class="flex flex-wrap gap-3 items-end">
				<div class="flex-1 min-w-32">
					<label class="block text-xs text-slate-500 mb-1">Name</label>
					<input
						type="text"
						bind:value={newAgentName}
						placeholder="e.g. Alice"
						class="w-full px-3 py-2 rounded-xl border border-slate-200 text-sm focus:outline-none focus:ring-2 focus:ring-vanna-teal/40 focus:border-vanna-teal transition-all"
						autofocus
					/>
				</div>
				<div>
					<label class="block text-xs text-slate-500 mb-1">Role</label>
					<select
						bind:value={newAgentRole}
						class="px-3 py-2 rounded-xl border border-slate-200 text-sm bg-white focus:outline-none focus:ring-2 focus:ring-vanna-teal/40 transition-all"
					>
						{#each ROLES as role}
							<option value={role.value}>{role.label}</option>
						{/each}
					</select>
				</div>
				<div class="flex-1 min-w-28">
					<label class="block text-xs text-slate-500 mb-1">Project (optional)</label>
					<input
						type="text"
						bind:value={newAgentProject}
						placeholder="e.g. BNX/briefhours"
						class="w-full px-3 py-2 rounded-xl border border-slate-200 text-sm focus:outline-none focus:ring-2 focus:ring-vanna-teal/40 transition-all"
					/>
				</div>
				<div class="min-w-48">
					<label class="block text-xs text-slate-500 mb-1">Repo path (for spawning)</label>
					<input
						type="text"
						bind:value={newAgentRepoPath}
						placeholder="e.g. /home/sbarker/git-bnx/BNX/app"
						class="w-full px-3 py-2 rounded-xl border border-slate-200 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-vanna-teal/40 transition-all"
					/>
				</div>
				<div class="flex gap-2">
					<button
						type="submit"
						disabled={addingAgent || !newAgentName.trim()}
						class="bg-vanna-teal text-white px-4 py-2 rounded-xl text-sm font-medium disabled:opacity-50 hover:bg-vanna-teal/90 transition-colors"
					>
						{addingAgent ? 'Adding…' : 'Add'}
					</button>
					<button
						type="button"
						onclick={() => (showAgentForm = false)}
						class="px-4 py-2 rounded-xl border border-slate-200 text-sm text-slate-500 hover:bg-vanna-cream/50 transition-colors"
					>
						Cancel
					</button>
				</div>
			</form>
		</div>
	{/if}

	<!-- Agent avatars -->
	{#if agents.length > 0}
		<div class="mb-6">
			<h2 class="text-xs font-semibold text-slate-400 uppercase tracking-wider mb-3">Agents</h2>
			<div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5 gap-3">
				{#each agents as agent}
					<div class="relative group/avatar">
						<AgentAvatar
							{agent}
							{tasks}
							{sessionId}
							onSpawn={spawnAgent}
							onDespawn={despawnAgent}
							onAddNote={addNote}
						/>
						<button
							type="button"
							aria-label="Remove agent"
							onclick={() => removeAgent(agent.id)}
							class="absolute top-2 right-2 opacity-0 group-hover/avatar:opacity-100 p-1 bg-white rounded-lg border border-slate-200 text-slate-300 hover:text-red-400 transition-all shadow-sm"
						>
							<svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
								<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
							</svg>
						</button>
					</div>
				{/each}
			</div>
		</div>
	{/if}

	<!-- Message panel (shown when any live workers exist) -->
	{#if liveWorkers.length > 0}
		<div class="mb-6">
			<h2 class="text-xs font-semibold text-slate-400 uppercase tracking-wider mb-3">Workers</h2>
			<OrchestratorPanel
				{sessionId}
				agents={agents}
				onMessage={sendToWorkers}
			/>
		</div>
	{/if}

	<!-- Kanban board -->
	<div>
		<h2 class="text-xs font-semibold text-slate-400 uppercase tracking-wider mb-3">Board</h2>
		<div class="grid grid-cols-1 sm:grid-cols-3 lg:grid-cols-5 gap-4 min-h-64">
			{#each STAGES as stage}
				{@const stageTasks = tasksInStage(stage)}
				<div class="rounded-2xl border {STAGE_COLORS[stage]} p-3 flex flex-col gap-2 min-h-48">
					<!-- Column header -->
					<div class="flex items-center justify-between mb-1 px-1">
						<h3 class="text-xs font-semibold text-slate-600 uppercase tracking-wider">
							{STAGE_LABELS[stage]}
						</h3>
						<span class="text-xs text-slate-400 bg-white/60 rounded-full px-1.5 py-0.5 min-w-5 text-center">
							{stageTasks.length}
						</span>
					</div>

					<!-- Task cards -->
					{#each stageTasks as task}
						<TaskCard
							{task}
							{agents}
							onMoveStage={moveTask}
							onDelete={deleteTask}
							onAssign={assignTask}
							onInjectBrief={injectTaskBrief}
							onCreatePR={createPR}
						/>
					{/each}

					<!-- Add task form / button -->
					{#if showTaskForm[stage]}
						<form
							onsubmit={(e) => { e.preventDefault(); addTask(stage); }}
							class="bg-white rounded-xl border border-slate-200 p-3 flex flex-col gap-2"
						>
							<input
								type="text"
								bind:value={newTaskTitle}
								placeholder="Task title…"
								class="w-full px-2 py-1.5 text-sm border border-slate-200 rounded-lg focus:outline-none focus:ring-2 focus:ring-vanna-teal/40 focus:border-vanna-teal transition-all"
								autofocus
							/>
							<input
								type="text"
								bind:value={newTaskProject}
								placeholder="Project (optional)"
								class="w-full px-2 py-1.5 text-xs border border-slate-200 rounded-lg focus:outline-none focus:ring-2 focus:ring-vanna-teal/40 transition-all"
							/>
							<div class="flex gap-2">
								<button
									type="submit"
									disabled={addingTask || !newTaskTitle.trim()}
									class="flex-1 bg-vanna-teal text-white py-1.5 rounded-lg text-xs font-medium disabled:opacity-50 hover:bg-vanna-teal/90 transition-colors"
								>
									{addingTask ? '…' : 'Add'}
								</button>
								<button
									type="button"
									onclick={() => (showTaskForm = {})}
									class="px-3 py-1.5 rounded-lg border border-slate-200 text-xs text-slate-500 hover:bg-vanna-cream/50 transition-colors"
								>
									✕
								</button>
							</div>
						</form>
					{:else}
						<button
							type="button"
							onclick={() => openTaskForm(stage)}
							class="w-full flex items-center justify-center gap-1.5 py-2 rounded-xl border border-dashed border-slate-300 text-xs text-slate-400 hover:border-vanna-teal/40 hover:text-vanna-teal hover:bg-white/60 transition-all mt-auto"
						>
							<svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
								<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" />
							</svg>
							Add task
						</button>
					{/if}
				</div>
			{/each}
		</div>
	</div>

	<!-- Activity feed -->
	<div class="mt-6">
		<EventFeed {events} {agents} />
	</div>
{/if}

{#if voiceOpen}
	<VoiceConversation sessionId={sessionId} onClose={() => (voiceOpen = false)} />
{/if}
