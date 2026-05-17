package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// helper: build oaiMessage with plain text content
func msg(role, text string) oaiMessage {
	b, _ := json.Marshal(text)
	return oaiMessage{Role: role, Content: b}
}

func TestBuildCachedContent_NoSystemNoHistory_ReturnsString(t *testing.T) {
	content, prompt := buildCachedContent([]oaiMessage{msg("user", "hi")})
	s, ok := content.(string)
	if !ok {
		t.Fatalf("expected string content, got %T", content)
	}
	if s != "hi" {
		t.Errorf("content = %q", s)
	}
	if prompt == "" {
		t.Error("prompt should be non-empty")
	}
}

func TestBuildCachedContent_SmallSystem_NoCacheBlocks(t *testing.T) {
	// Small system → inlined as string, no cache_control
	content, _ := buildCachedContent([]oaiMessage{
		msg("system", "be concise"),
		msg("user", "hello"),
	})
	s, ok := content.(string)
	if !ok {
		t.Fatalf("expected string content (small system), got %T", content)
	}
	if !strings.Contains(s, "be concise") || !strings.Contains(s, "hello") {
		t.Errorf("content missing parts: %q", s)
	}
}

func TestBuildCachedContent_LargeSystem_AddsCacheControl(t *testing.T) {
	largeSystem := strings.Repeat("You are a helpful assistant. ", 250) // ~7250 chars
	content, _ := buildCachedContent([]oaiMessage{
		msg("system", largeSystem),
		msg("user", "what is 2+2?"),
	})
	blocks, ok := content.([]map[string]interface{})
	if !ok {
		t.Fatalf("expected []map content for large system, got %T", content)
	}
	if len(blocks) < 2 {
		t.Fatalf("expected >=2 blocks, got %d", len(blocks))
	}
	// First block is system with cache_control
	if cc, has := blocks[0]["cache_control"]; !has {
		t.Errorf("system block missing cache_control: %+v", blocks[0])
	} else {
		ccMap := cc.(map[string]string)
		if ccMap["ttl"] != "1h" {
			t.Errorf("expected ttl=1h, got %v", ccMap)
		}
	}
	// Last block (current user) must NOT have cache_control
	last := blocks[len(blocks)-1]
	if _, has := last["cache_control"]; has {
		t.Errorf("current-user block should not be cached: %+v", last)
	}
	text := last["text"].(string)
	if text != "what is 2+2?" {
		t.Errorf("current user text = %q", text)
	}
}

func TestBuildCachedContent_LargeHistory_AddsCacheControl(t *testing.T) {
	largeAssist := strings.Repeat("Long assistant turn. ", 350) // ~7350 chars
	content, _ := buildCachedContent([]oaiMessage{
		msg("user", "first question"),
		msg("assistant", largeAssist),
		msg("user", "follow up"),
	})
	blocks, ok := content.([]map[string]interface{})
	if !ok {
		t.Fatalf("expected []map content for large history, got %T", content)
	}
	// Find the history block — should have cache_control
	found := false
	for _, b := range blocks {
		text, _ := b["text"].(string)
		if strings.Contains(text, "Long assistant turn") {
			if _, has := b["cache_control"]; !has {
				t.Errorf("large history block missing cache_control: %+v", b)
			}
			found = true
		}
	}
	if !found {
		t.Error("history block not found in blocks")
	}
}

func TestBuildCachedContent_LargeSystemAndHistory_TwoCacheBlocks(t *testing.T) {
	largeSystem := strings.Repeat("System rules. ", 600)    // ~8400 chars
	largeAssist := strings.Repeat("Prior context. ", 600) // ~9000 chars
	content, _ := buildCachedContent([]oaiMessage{
		msg("system", largeSystem),
		msg("user", "q1"),
		msg("assistant", largeAssist),
		msg("user", "q2"),
	})
	blocks, ok := content.([]map[string]interface{})
	if !ok {
		t.Fatalf("expected []map content, got %T", content)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks (system+history+current), got %d", len(blocks))
	}
	cached := 0
	for _, b := range blocks {
		if _, has := b["cache_control"]; has {
			cached++
		}
	}
	if cached != 2 {
		t.Errorf("expected 2 cache_control blocks, got %d (Anthropic allows ≤4 incl Claude Code's own)", cached)
	}
}
