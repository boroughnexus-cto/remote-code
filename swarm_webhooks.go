package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// WebhookEvent is the payload POSTed to the configured n8n webhook URL.
type WebhookEvent struct {
	Event     string      `json:"event"`
	Timestamp int64       `json:"timestamp"`
	Data      interface{} `json:"data"`
}

// fireWebhook sends a fire-and-forget POST to the configured n8n webhook URL.
// If no webhook URL is configured, it is a no-op.
func fireWebhook(eventType string, data interface{}) {
	if globalConfigService == nil {
		return
	}
	url := globalConfigService.GetString("n8n.webhook_url", "")
	if url == "" {
		return
	}

	payload := WebhookEvent{
		Event:     eventType,
		Timestamp: time.Now().Unix(),
		Data:      data,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("webhook: marshal error for event %q: %v", eventType, err)
		return
	}

	go func() {
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("webhook: POST %s event=%q: %v", url, eventType, err)
			return
		}
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			log.Printf("webhook: POST %s event=%q: HTTP %d", url, eventType, resp.StatusCode)
		}
	}()
}
