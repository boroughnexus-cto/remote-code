<script lang="ts">
	import { STAGES, STAGE_LABELS, STAGE_DOT_COLORS } from '$lib/workflowStages';

	interface Props {
		tasksByStage: Record<string, number>;
	}

	let { tasksByStage }: Props = $props();

	let total = $derived(STAGES.reduce((s, k) => s + (tasksByStage[k] ?? 0), 0));
</script>

{#if total > 0}
	<div class="flex gap-0.5 rounded-full overflow-hidden h-2" title="{total} task{total !== 1 ? 's' : ''}">
		{#each STAGES as stage}
			{#if (tasksByStage[stage] ?? 0) > 0}
				<div
					class="{STAGE_DOT_COLORS[stage]} transition-all"
					style="width: {((tasksByStage[stage] ?? 0) / total) * 100}%"
					title="{tasksByStage[stage]} {STAGE_LABELS[stage] ?? stage}"
				></div>
			{/if}
		{/each}
	</div>
	<div class="flex gap-3 mt-1">
		{#each STAGES as stage}
			{#if (tasksByStage[stage] ?? 0) > 0}
				<span class="text-xs text-slate-400">
					<span class="inline-block w-2 h-2 rounded-sm {STAGE_DOT_COLORS[stage]} mr-0.5 translate-y-px"></span>
					{tasksByStage[stage]} {STAGE_LABELS[stage] ?? stage}
				</span>
			{/if}
		{/each}
	</div>
{:else}
	<p class="text-xs text-slate-300 italic">No tasks yet</p>
{/if}
