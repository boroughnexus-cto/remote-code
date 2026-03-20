// Canonical Talos workflow stage definitions — single source of truth.
// Import this in any component that needs to display or reason about task stages.

export const STAGES = [
	'spec',
	'plan',
	'plan_review',
	'implement',
	'impl_review',
	'judge',
	'deploy',
	'document'
] as const;

export type Stage = (typeof STAGES)[number];

export const STAGE_LABELS: Record<string, string> = {
	spec:        'Spec',
	plan:        'Plan',
	plan_review: 'Plan Review',
	implement:   'Implement',
	impl_review: 'Impl Review',
	judge:       'Judge',
	deploy:      'Deploy',
	document:    'Document'
};

// Kanban column border/background colours (for full kanban view)
export const STAGE_COLORS: Record<string, string> = {
	spec:        'border-blue-200 bg-blue-50/50',
	plan:        'border-indigo-200 bg-indigo-50/50',
	plan_review: 'border-violet-200 bg-violet-50/50',
	implement:   'border-vanna-teal/30 bg-vanna-teal/5',
	impl_review: 'border-cyan-200 bg-cyan-50/50',
	judge:       'border-amber-200 bg-amber-50/50',
	deploy:      'border-orange-200 bg-orange-50/50',
	document:    'border-green-200 bg-green-50/50'
};

// Mini-bar dot colours (for KanbanMiniBar)
export const STAGE_DOT_COLORS: Record<string, string> = {
	spec:        'bg-blue-400',
	plan:        'bg-indigo-400',
	plan_review: 'bg-violet-400',
	implement:   'bg-vanna-teal',
	impl_review: 'bg-cyan-400',
	judge:       'bg-amber-400',
	deploy:      'bg-orange-400',
	document:    'bg-green-500'
};
