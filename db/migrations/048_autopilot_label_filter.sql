-- Migration 048: autopilot label filter
-- Adds an optional Plane label ID filter to autopilot sessions.
-- When set, only Plane issues tagged with this label are synced as goals.
ALTER TABLE swarm_sessions ADD COLUMN autopilot_label_filter TEXT DEFAULT NULL;
