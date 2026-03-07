-- name: CreateExecutionMilestone :one
INSERT INTO execution_milestones (execution_id, text)
VALUES (?, ?)
RETURNING *;

-- name: ListMilestonesByExecutionID :many
SELECT * FROM execution_milestones
WHERE execution_id = ?
ORDER BY created_at DESC
LIMIT 10;

-- name: GetRecentMilestoneTexts :many
SELECT text FROM execution_milestones
WHERE execution_id = ?
ORDER BY created_at DESC
LIMIT 10;
