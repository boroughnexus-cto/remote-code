package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestRouter creates a TelegramRouter with no live goroutines for unit tests.
func newTestRouter() *TelegramRouter {
	return &TelegramRouter{
		botToken: "test-token",
		chatID:   12345,
		updates:  make(chan tgUpdate, 256),
		stopc:    make(chan struct{}),
	}
}

// -----------------
// HandleWebhook tests (no DB required)
// -----------------

func TestHandleWebhook_QueuesUpdate(t *testing.T) {
	tr := newTestRouter()

	body, _ := json.Marshal(tgUpdate{
		UpdateID: 42,
		Message: &tgMessage{
			MessageID: 1,
			Chat:      tgChat{ID: 12345},
			Text:      "hello",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/telegram/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	tr.HandleWebhook(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	select {
	case got := <-tr.updates:
		if got.UpdateID != 42 {
			t.Errorf("expected UpdateID=42, got %d", got.UpdateID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("update not queued within 100ms")
	}
}

func TestHandleWebhook_WrongMethod(t *testing.T) {
	tr := newTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/telegram/webhook", nil)
	rr := httptest.NewRecorder()
	tr.HandleWebhook(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestHandleWebhook_RejectsBadSecret(t *testing.T) {
	tr := newTestRouter()
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "correct-secret")

	body, _ := json.Marshal(tgUpdate{UpdateID: 1})
	req := httptest.NewRequest(http.MethodPost, "/api/telegram/webhook", bytes.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "wrong-secret")
	rr := httptest.NewRecorder()

	tr.HandleWebhook(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

func TestHandleWebhook_AcceptsCorrectSecret(t *testing.T) {
	tr := newTestRouter()
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "correct-secret")

	body, _ := json.Marshal(tgUpdate{UpdateID: 99})
	req := httptest.NewRequest(http.MethodPost, "/api/telegram/webhook", bytes.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "correct-secret")
	rr := httptest.NewRecorder()

	tr.HandleWebhook(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestHandleWebhook_IgnoresInvalidJSON(t *testing.T) {
	tr := newTestRouter()
	req := httptest.NewRequest(http.MethodPost, "/api/telegram/webhook", bytes.NewReader([]byte("not json{")))
	rr := httptest.NewRecorder()
	tr.HandleWebhook(rr, req)
	// Must return 200 (Telegram re-delivers on non-2xx).
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 on invalid JSON, got %d", rr.Code)
	}
}

func TestHandleWebhook_DropWhenFull(t *testing.T) {
	tr := newTestRouter()
	// Fill the queue to capacity.
	for i := 0; i < 256; i++ {
		tr.updates <- tgUpdate{UpdateID: int64(i)}
	}
	// One more should be silently dropped — no panic, returns 200.
	body, _ := json.Marshal(tgUpdate{UpdateID: 999})
	req := httptest.NewRequest(http.MethodPost, "/api/telegram/webhook", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	tr.HandleWebhook(rr, req) // must not block or panic
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 on full queue, got %d", rr.Code)
	}
}

// -----------------
// processUpdate tests (no DB: only non-reply paths)
// -----------------

func TestProcessUpdate_NilMessage(t *testing.T) {
	tr := newTestRouter()
	// Must not panic on a bare update with no message.
	tr.processUpdate(nil, tgUpdate{UpdateID: 1}) //nolint:staticcheck
}

func TestProcessUpdate_NoReply(t *testing.T) {
	tr := newTestRouter()
	// A top-level message (not a reply) must be silently skipped.
	update := tgUpdate{
		UpdateID: 2,
		Message: &tgMessage{
			MessageID: 10,
			Chat:      tgChat{ID: 12345},
			Text:      "hello",
			ReplyTo:   nil, // not a reply
		},
	}
	tr.processUpdate(nil, update) //nolint:staticcheck
}

func TestProcessUpdate_EmptyReplyText(t *testing.T) {
	tr := newTestRouter()
	// A reply with blank text must be skipped (e.g. sticker reply).
	update := tgUpdate{
		UpdateID: 3,
		Message: &tgMessage{
			MessageID: 20,
			Chat:      tgChat{ID: 12345},
			Text:      "   ", // blank after trim
			ReplyTo:   &tgMessage{MessageID: 5},
		},
	}
	tr.processUpdate(nil, update) //nolint:staticcheck
}

// -----------------
// formatEscalationText tests
// -----------------

func TestFormatEscalationText_ContainsQuestion(t *testing.T) {
	text := formatEscalationText("agentXYZW1234", "task-abc12345", "What should I do?")
	for _, want := range []string{"agentXYZ", "task-abc", "What should I do?", "Reply to this message"} {
		if !containsStr(text, want) {
			t.Errorf("expected %q in escalation text, got:\n%s", want, text)
		}
	}
}

func TestFormatEscalationText_NoTaskID(t *testing.T) {
	text := formatEscalationText("agent-1", "", "My question")
	if containsStr(text, "Task:") {
		t.Errorf("should omit Task line when taskID is empty, got:\n%s", text)
	}
}

// -----------------
// newEscalationID tests
// -----------------

func TestNewEscalationID_Prefix(t *testing.T) {
	id := newEscalationID()
	if !containsStr(id, "esc-") {
		t.Errorf("expected esc- prefix, got %q", id)
	}
}

func TestNewEscalationID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := newEscalationID()
		if seen[id] {
			t.Fatalf("duplicate escalation ID: %q", id)
		}
		seen[id] = true
	}
}

// -----------------
// truncateID tests
// -----------------

func TestTruncateID_Short(t *testing.T) {
	if got := truncateID("abc"); got != "abc" {
		t.Errorf("got %q, want %q", got, "abc")
	}
}

func TestTruncateID_Long(t *testing.T) {
	if got := truncateID("abcdefghijklmnop"); got != "abcdefgh" {
		t.Errorf("got %q, want %q", got, "abcdefgh")
	}
}

func TestTruncateID_Exactly8(t *testing.T) {
	if got := truncateID("12345678"); got != "12345678" {
		t.Errorf("got %q, want %q", got, "12345678")
	}
}

// -----------------
// initTelegramRouter tests
// -----------------

func TestInitTelegramRouter_DisabledWhenNoEnv(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")
	if got := initTelegramRouter(); got != nil {
		got.Stop()
		t.Error("expected nil router when env vars absent")
	}
}

func TestInitTelegramRouter_DisabledOnBadChatID(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "some-token")
	t.Setenv("TELEGRAM_CHAT_ID", "not-an-integer")
	if got := initTelegramRouter(); got != nil {
		got.Stop()
		t.Error("expected nil router on invalid chat ID")
	}
}

// -----------------
// helpers
// -----------------

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && stringContains(s, sub))
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
