<script lang="ts">
	interface Agent {
		id: string;
		name: string;
		role: string;
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

	interface Props {
		events: SwarmEvent[];
		agents: Agent[];
	}

	let { events, agents }: Props = $props();

	const eventConfig: Record<string, { icon: string; color: string; label: string }> = {
		agent_spawned:        { icon: '🟢', color: 'text-vanna-teal',    label: 'Spawned'       },
		agent_despawned:      { icon: '⭕', color: 'text-slate-400',     label: 'Stopped'       },
		agent_offline:        { icon: '🔴', color: 'text-red-400',       label: 'Went offline'  },
		agent_stuck:          { icon: '⚠️', color: 'text-red-500',       label: 'Stuck'         },
		agent_waiting:        { icon: '⏸️', color: 'text-orange-500',    label: 'Waiting'       },
		task_created:         { icon: '📋', color: 'text-blue-500',      label: 'Task created'  },
		task_moved:           { icon: '➡️', color: 'text-vanna-teal',    label: 'Task moved'    },
		inject_brief:         { icon: '⚡', color: 'text-vanna-teal',    label: 'Brief sent'    },
		orchestrator_message: { icon: '💬', color: 'text-vanna-magenta', label: 'User message'  },
		session_resumed:      { icon: '▶️', color: 'text-green-500',     label: 'Resumed'       },
	};

	function agentName(agentID: string | undefined): string {
		if (!agentID) return '';
		return agents.find((a) => a.id === agentID)?.name ?? agentID.slice(0, 8);
	}

	function relativeTime(ts: number): string {
		const diff = Math.floor(Date.now() / 1000) - ts;
		if (diff < 60) return `${diff}s ago`;
		if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
		if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
		return `${Math.floor(diff / 86400)}d ago`;
	}

	function eventDescription(e: SwarmEvent): string {
		const cfg = eventConfig[e.type];
		const name = agentName(e.agent_id);
		const prefix = name ? `${name}: ` : '';

		switch (e.type) {
			case 'agent_spawned':        return `${prefix}spawned`;
			case 'agent_despawned':      return `${prefix}stopped`;
			case 'agent_offline':        return `${prefix}went offline`;
			case 'agent_stuck':          return `${prefix}is stuck`;
			case 'agent_waiting':        return `${prefix}is waiting for input`;
			case 'task_created':         return `New task: ${e.payload ?? ''}`;
			case 'task_moved':           return `Task → ${e.payload ?? ''}`;
			case 'inject_brief':         return `${prefix}received task brief`;
			case 'orchestrator_message': return `You: ${e.payload ?? ''}`;
			case 'session_resumed':      return e.payload ?? 'Session resumed';
			default:                     return `${prefix}${e.type}`;
		}
	}
</script>

<div class="bg-white/80 rounded-2xl border border-slate-200 shadow-sm p-4">
	<h3 class="text-xs font-semibold text-slate-400 uppercase tracking-wider mb-3">Activity</h3>

	{#if events.length === 0}
		<p class="text-xs text-slate-300 italic text-center py-4">No activity yet</p>
	{:else}
		<div class="space-y-1 max-h-64 overflow-y-auto">
			{#each events as event (event.id)}
				{@const cfg = eventConfig[event.type] ?? { icon: '·', color: 'text-slate-400', label: event.type }}
				<div class="flex items-start gap-2 text-xs py-1 border-b border-slate-50 last:border-0">
					<span class="flex-shrink-0 w-5 text-center leading-4">{cfg.icon}</span>
					<span class="flex-1 {cfg.color} leading-4 truncate">{eventDescription(event)}</span>
					<span class="flex-shrink-0 text-slate-300 whitespace-nowrap">{relativeTime(event.ts)}</span>
				</div>
			{/each}
		</div>
	{/if}
</div>
