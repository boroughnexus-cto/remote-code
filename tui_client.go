package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// swarmClient is the interface the TUI uses to talk to the SwarmOps backend.
// The concrete implementation is apiClient (HTTP). Tests use fakeSwarmClient.
type swarmClient interface {
	Spawn(ctx context.Context, name, dir string, contextID, contextName, mission *string, model string) (*Session, error)
	listSessions() ([]Session, error)
	deleteSession(id string) error
	renameSession(id, name string) error
	poolStatus() (map[string]interface{}, error)
	getConfig(key string) (string, error)
	setMission(id, mission string) error
	listAuditEvents(limit int) ([]ManagedSessionEvent, error)
	healthCheck() error
}

// apiClient is an HTTP client for the SwarmOps backend API.
// Used by the TUI in client mode instead of direct in-process calls.
type apiClient struct {
	baseURL string
	http    *http.Client
}

func newAPIClient(baseURL string) *apiClient {
	return &apiClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Spawn implements the Spawner interface via the HTTP API.
func (c *apiClient) Spawn(ctx context.Context, name, dir string, contextID, contextName, mission *string, model string) (*Session, error) {
	body := map[string]interface{}{
		"name":      name,
		"directory": dir,
	}
	if contextID != nil {
		body["context_id"] = *contextID
	}
	if mission != nil {
		body["mission"] = *mission
	}
	if model != "" {
		body["model"] = model
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/swarm/sessions", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API %d: %s", resp.StatusCode, string(respBody))
	}

	var s Session
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &s, nil
}

func (c *apiClient) listSessions() ([]Session, error) {
	resp, err := c.http.Get(c.baseURL + "/api/swarm/sessions")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var sessions []Session
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

func (c *apiClient) deleteSession(id string) error {
	req, err := http.NewRequest("DELETE", c.baseURL+"/api/swarm/sessions/"+id, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("API %d", resp.StatusCode)
	}
	return nil
}

func (c *apiClient) renameSession(id, name string) error {
	data, _ := json.Marshal(map[string]string{"name": name})
	req, err := http.NewRequest("PATCH", c.baseURL+"/api/swarm/sessions/"+id, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("API %d", resp.StatusCode)
	}
	return nil
}

// poolStatus returns the pool status as a map, handling JSON number type
// conversion (JSON numbers unmarshal as float64, not int64).
func (c *apiClient) poolStatus() (map[string]interface{}, error) {
	resp, err := c.http.Get(c.baseURL + "/api/swarm/pool")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var status map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}
	return status, nil
}

func (c *apiClient) getConfig(key string) (string, error) {
	resp, err := c.http.Get(c.baseURL + "/api/swarm/config/" + key)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var entry struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entry); err != nil {
		return "", err
	}
	return entry.Value, nil
}

func (c *apiClient) setMission(id, mission string) error {
	data, _ := json.Marshal(map[string]interface{}{"mission": mission})
	req, err := http.NewRequest("PATCH", c.baseURL+"/api/swarm/sessions/"+id, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("API %d", resp.StatusCode)
	}
	return nil
}

func (c *apiClient) listAuditEvents(limit int) ([]ManagedSessionEvent, error) {
	resp, err := c.http.Get(fmt.Sprintf("%s/api/swarm/audit?limit=%d", c.baseURL, limit))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var events []ManagedSessionEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, err
	}
	return events, nil
}

// healthCheck verifies the backend is reachable.
func (c *apiClient) healthCheck() error {
	resp, err := c.http.Get(c.baseURL + "/")
	if err != nil {
		return fmt.Errorf("cannot reach SwarmOps backend at %s: %w", c.baseURL, err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("backend returned %d", resp.StatusCode)
	}
	return nil
}
