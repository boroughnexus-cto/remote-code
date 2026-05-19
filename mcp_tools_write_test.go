package main

import (
	"context"
	"testing"
)

// TestCreateSession_AllFields verifies that the full set of fields exposed by
// the extended rc_run_task MCP tool (name, directory, context_id, context_name,
// mission, model) round-trips correctly through createSession into the DB.
//
// This is the truth source for the MCP plumbing — rc_run_task is a thin
// pass-through above this layer.
func TestCreateSession_AllFields(t *testing.T) {
	defer setupTestDB(t)()
	ctx := context.Background()

	ctxID := "ctx-uuid-12345"
	ctxName := "TKN Home Infra"
	mission := "Investigate the Icinga alert"
	model := "claude-sonnet-4-6"

	sess, err := createSession(ctx, "my-named-session", "/tmp/work-dir",
		&ctxID, &ctxName, &mission, false, model)
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}

	// Verify returned session has all fields
	if sess.Name != "my-named-session" {
		t.Errorf("Name = %q, want %q", sess.Name, "my-named-session")
	}
	if sess.Directory != "/tmp/work-dir" {
		t.Errorf("Directory = %q, want %q", sess.Directory, "/tmp/work-dir")
	}
	if sess.ContextID == nil || *sess.ContextID != ctxID {
		t.Errorf("ContextID = %v, want %q", sess.ContextID, ctxID)
	}
	if sess.ContextName == nil || *sess.ContextName != ctxName {
		t.Errorf("ContextName = %v, want %q", sess.ContextName, ctxName)
	}
	if sess.Mission == nil || *sess.Mission != mission {
		t.Errorf("Mission = %v, want %q", sess.Mission, mission)
	}
	if sess.Model != model {
		t.Errorf("Model = %q, want %q", sess.Model, model)
	}

	// Round-trip through getSession to verify DB persistence
	got, err := getSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("getSession: %v", err)
	}
	if got.ContextID == nil || *got.ContextID != ctxID {
		t.Errorf("persisted ContextID = %v, want %q", got.ContextID, ctxID)
	}
	if got.ContextName == nil || *got.ContextName != ctxName {
		t.Errorf("persisted ContextName = %v, want %q", got.ContextName, ctxName)
	}
	if got.Model != model {
		t.Errorf("persisted Model = %q, want %q", got.Model, model)
	}
}

// TestCreateSession_NilOptionals verifies the original 3-field call path
// (no context, no mission, no model) still works and stores nulls correctly.
func TestCreateSession_NilOptionals(t *testing.T) {
	defer setupTestDB(t)()
	ctx := context.Background()

	sess, err := createSession(ctx, "minimal", "/tmp", nil, nil, nil, false, "")
	if err != nil {
		t.Fatalf("createSession: %v", err)
	}
	if sess.ContextID != nil {
		t.Errorf("ContextID = %v, want nil", sess.ContextID)
	}
	if sess.ContextName != nil {
		t.Errorf("ContextName = %v, want nil", sess.ContextName)
	}
	if sess.Mission != nil {
		t.Errorf("Mission = %v, want nil", sess.Mission)
	}
	if sess.Model != "" {
		t.Errorf("Model = %q, want empty", sess.Model)
	}

	got, err := getSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("getSession: %v", err)
	}
	if got.ContextID != nil || got.ContextName != nil || got.Mission != nil {
		t.Errorf("expected all nil, got ContextID=%v ContextName=%v Mission=%v", got.ContextID, got.ContextName, got.Mission)
	}
}
