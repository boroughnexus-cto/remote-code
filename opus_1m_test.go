package main

import "testing"

func TestModelShortName_Opus1M(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"claude-haiku-4-5", "haiku"},
		{"claude-sonnet-4-6", "sonnet"},
		{"claude-opus-4-6", "opus"},
		{"claude-opus-4-6[1m]", "opus1m"},
		{"claude-sonnet-4-5-1m", "sonnet1m"}, // also recognise the -1m suffix style
	}
	for _, c := range cases {
		if got := modelShortName(c.model); got != c.want {
			t.Errorf("modelShortName(%q) = %q, want %q", c.model, got, c.want)
		}
	}
}

func TestModelAliases_Opus1M(t *testing.T) {
	cases := []struct{ alias, want string }{
		{"opus1m", "claude-opus-4-6[1m]"},
		{"opus-1m", "claude-opus-4-6[1m]"},
		{"OPUS1M", "claude-opus-4-6[1m]"}, // case-insensitive
		{"claude-opus-4-6[1m]", "claude-opus-4-6[1m]"},
		{"opus", "claude-opus-4-6"}, // base opus unchanged
	}
	for _, c := range cases {
		got, ok := resolveModel(c.alias)
		if !ok {
			t.Errorf("resolveModel(%q) = !ok", c.alias)
			continue
		}
		if got != c.want {
			t.Errorf("resolveModel(%q) = %q, want %q", c.alias, got, c.want)
		}
	}
}

func TestModelContextLengths_Opus1M(t *testing.T) {
	if got := modelContextLengths["claude-opus-4-6[1m]"]; got != 1_000_000 {
		t.Errorf("opus 1M context = %d, want 1000000", got)
	}
	if got := modelContextLengths["claude-opus-4-6"]; got != 200_000 {
		t.Errorf("opus base context = %d, want 200000", got)
	}
}
