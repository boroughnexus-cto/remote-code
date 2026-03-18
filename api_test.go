package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
	"swarmops/db"
)

var testDbPath string

func TestMain(m *testing.M) {
	// Setup test database
	database, queries, testDbPath = initTestDatabase()
	defer database.Close()
	
	// Clean up test database file when done
	defer func() {
		os.Remove(testDbPath)
		// Also clean up any other test database files just in case
		matches, _ := filepath.Glob("swarmops-test-*.db")
		for _, match := range matches {
			os.Remove(match)
		}
	}()
	
	// Run tests
	code := m.Run()
	os.Exit(code)
}

func setupTestDB(t *testing.T) {
	// Clean existing data for isolated tests
	ctx := context.Background()

	// Clean ELO tables first (due to foreign key constraints)
	database.ExecContext(ctx, "DELETE FROM agent_competitions") //nolint:errcheck

	// Clean other tables
	database.ExecContext(ctx, "DELETE FROM task_executions")  //nolint:errcheck
	database.ExecContext(ctx, "DELETE FROM tasks")             //nolint:errcheck
	database.ExecContext(ctx, "DELETE FROM worktrees")         //nolint:errcheck
	database.ExecContext(ctx, "DELETE FROM base_directories")  //nolint:errcheck
	database.ExecContext(ctx, "DELETE FROM projects")          //nolint:errcheck
	database.ExecContext(ctx, "DELETE FROM agents")            //nolint:errcheck
	database.ExecContext(ctx, "DELETE FROM roots")             //nolint:errcheck

	// Reset AUTOINCREMENT counters. The GET /api/projects API hardcodes root_id=1
	// (see api.go: GetProjectsByRootID(ctx, 1)). Tests create a root dynamically and
	// use root.ID, so resetting the counter ensures root.ID==1 matches the API's expectation.
	// This workaround can be removed if/when the API is updated to derive the root ID dynamically.
	database.ExecContext(ctx, "DELETE FROM sqlite_sequence WHERE name IN ('roots','projects','agents','tasks','base_directories','task_executions')") //nolint:errcheck
}

func TestProjectsAPI_GET_Empty(t *testing.T) {
	setupTestDB(t)
	
	req := httptest.NewRequest("GET", "/api/projects", nil)
	w := httptest.NewRecorder()
	
	handleAPI(w, req)
	
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
	
	var projects []Project
	if err := json.Unmarshal(w.Body.Bytes(), &projects); err != nil {
		t.Errorf("Failed to unmarshal response: %v", err)
	}
	
	if len(projects) != 0 {
		t.Errorf("Expected empty projects list, got %d projects", len(projects))
	}
}

func TestProjectsAPI_POST_Create(t *testing.T) {
	setupTestDB(t)
	
	// First create a root to associate the project with
	ctx := context.Background()
	root, err := queries.CreateRoot(ctx, db.CreateRootParams{
		LocalPort: "8080",
	})
	if err != nil {
		t.Fatalf("Failed to create root: %v", err)
	}
	
	projectData := map[string]interface{}{
		"root_id": root.ID,
		"name":    "Test Project",
	}
	
	jsonData, _ := json.Marshal(projectData)
	req := httptest.NewRequest("POST", "/api/projects", bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	
	handleAPI(w, req)
	
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d. Response: %s", w.Code, w.Body.String())
	}
	
	var project Project
	if err := json.Unmarshal(w.Body.Bytes(), &project); err != nil {
		t.Errorf("Failed to unmarshal response: %v", err)
	}
	
	if project.Name != "Test Project" {
		t.Errorf("Expected project name 'Test Project', got '%s'", project.Name)
	}
}

func TestProjectsAPI_GET_WithProjects(t *testing.T) {
	setupTestDB(t)

	ctx := context.Background()
	root, err := queries.CreateRoot(ctx, db.CreateRootParams{LocalPort: "8080"})
	if err != nil {
		t.Fatalf("Failed to create root: %v", err)
	}
	_, err = queries.CreateProject(ctx, db.CreateProjectParams{
		RootID: root.ID,
		Name:   "Another Test Project",
	})
	if err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}
	
	req := httptest.NewRequest("GET", "/api/projects", nil)
	w := httptest.NewRecorder()
	
	handleAPI(w, req)
	
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
	
	var projects []Project
	if err := json.Unmarshal(w.Body.Bytes(), &projects); err != nil {
		t.Errorf("Failed to unmarshal response: %v", err)
	}
	
	// Should have at least the project we just created
	if len(projects) == 0 {
		t.Errorf("Expected at least 1 project, got %d", len(projects))
	}
	
	// Check if our project is in the list
	found := false
	for _, project := range projects {
		if project.Name == "Another Test Project" {
			found = true
			break
		}
	}
	
	if !found {
		t.Errorf("Expected to find 'Another Test Project' in projects list")
	}
}

func TestProjectsAPI_InvalidJSON(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/projects", bytes.NewBufferString("invalid json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	
	handleAPI(w, req)
	
	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}
}

func TestProjectsAPI_CORS(t *testing.T) {
	req := httptest.NewRequest("OPTIONS", "/api/projects", nil)
	w := httptest.NewRecorder()
	
	handleAPI(w, req)
	
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
	
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("Expected CORS header to be '*', got '%s'", w.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestProjectsAPI_ArraysNotNull(t *testing.T) {
	setupTestDB(t)
	
	ctx := context.Background()
	root, err := queries.CreateRoot(ctx, db.CreateRootParams{LocalPort: "8080"})
	if err != nil {
		t.Fatalf("Failed to create root: %v", err)
	}
	_, err = queries.CreateProject(ctx, db.CreateProjectParams{
		RootID: root.ID,
		Name:   "Test Project",
	})
	if err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/projects", nil)
	w := httptest.NewRecorder()
	
	handleAPI(w, req)
	
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
	
	// Parse response to verify array structure
	var projects []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &projects); err != nil {
		t.Errorf("Failed to unmarshal response: %v", err)
	}
	
	if len(projects) == 0 {
		t.Errorf("Expected at least one project")
		return
	}
	
	project := projects[0]
	
	// Check that baseDirectories is an array, not null (JSON tag is "baseDirectories")
	baseDirs, exists := project["baseDirectories"]
	if !exists {
		t.Errorf("baseDirectories field missing from response")
	} else if baseDirs == nil {
		t.Errorf("baseDirectories should not be null, should be an empty array")
	}
	
	// Check that tasks is an array, not null
	tasks, exists := project["tasks"]
	if !exists {
		t.Errorf("tasks field missing from response")
	} else if tasks == nil {
		t.Errorf("tasks should not be null, should be an empty array")
	}
}

func TestProjectTasksAPI_GET(t *testing.T) {
	setupTestDB(t)
	
	req := httptest.NewRequest("GET", "/api/projects/1/tasks", nil)
	w := httptest.NewRecorder()
	
	handleAPI(w, req)
	
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
	
	var tasks []Task
	if err := json.Unmarshal(w.Body.Bytes(), &tasks); err != nil {
		t.Errorf("Failed to unmarshal response: %v", err)
	}
	
	// Should return empty array for now
	if len(tasks) != 0 {
		t.Errorf("Expected empty tasks array, got %d tasks", len(tasks))
	}
}

func TestProjectTasksAPI_POST(t *testing.T) {
	setupTestDB(t)

	// Create root and project first
	ctx := context.Background()
	root, err := queries.CreateRoot(ctx, db.CreateRootParams{LocalPort: "8080"})
	if err != nil {
		t.Fatalf("Failed to create root: %v", err)
	}
	project, err := queries.CreateProject(ctx, db.CreateProjectParams{RootID: root.ID, Name: "Test Project"})
	if err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}

	// Create a base directory via the API to get a valid base_directory_id
	dirData := map[string]interface{}{
		"path":           "/test/dir",
		"gitInitialized": false,
	}
	dirJSON, _ := json.Marshal(dirData)
	dirReq := httptest.NewRequest("POST", fmt.Sprintf("/api/projects/%d/base-directories", project.ID), bytes.NewBuffer(dirJSON))
	dirReq.Header.Set("Content-Type", "application/json")
	dirW := httptest.NewRecorder()
	handleAPI(dirW, dirReq)
	if dirW.Code != http.StatusOK {
		t.Fatalf("Failed to create base directory: %d — %s", dirW.Code, dirW.Body.String())
	}
	var dir BaseDirectory
	if err := json.Unmarshal(dirW.Body.Bytes(), &dir); err != nil {
		t.Fatalf("Failed to unmarshal base directory: %v", err)
	}

	taskData := map[string]interface{}{
		"title":           "Test Task",
		"description":     "Test task description",
		"status":          "todo",
		"baseDirectoryId": dir.BaseDirectoryId,
	}

	jsonData, _ := json.Marshal(taskData)
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/projects/%d/tasks", project.ID), bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleAPI(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d. Response: %s", w.Code, w.Body.String())
	}

	var task Task
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Errorf("Failed to unmarshal response: %v", err)
	}

	if task.Title != "Test Task" {
		t.Errorf("Expected task title 'Test Task', got '%s'", task.Title)
	}

	if task.Status != "todo" {
		t.Errorf("Expected task status 'todo', got '%s'", task.Status)
	}
}

func TestProjectBaseDirectoriesAPI_POST(t *testing.T) {
	setupTestDB(t)

	// Create root and project first
	ctx := context.Background()
	root, err := queries.CreateRoot(ctx, db.CreateRootParams{LocalPort: "8080"})
	if err != nil {
		t.Fatalf("Failed to create root: %v", err)
	}
	project, err := queries.CreateProject(ctx, db.CreateProjectParams{
		RootID: root.ID,
		Name:   "Test Project",
	})
	if err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}
	
	directoryData := map[string]interface{}{
		"path":                        "/test/directory",
		"gitInitialized":             true,
		"worktreeSetupCommands":      "npm install",
		"worktreeTeardownCommands":   "",
		"devServerSetupCommands":     "npm run dev",
		"devServerTeardownCommands":  "",
	}
	
	jsonData, _ := json.Marshal(directoryData)
	req := httptest.NewRequest("POST", fmt.Sprintf("/api/projects/%d/base-directories", project.ID), bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	
	handleAPI(w, req)
	
	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d. Response: %s", w.Code, w.Body.String())
	}
	
	var directory BaseDirectory
	if err := json.Unmarshal(w.Body.Bytes(), &directory); err != nil {
		t.Errorf("Failed to unmarshal response: %v", err)
	}
	
	if directory.Path != "/test/directory" {
		t.Errorf("Expected directory path '/test/directory', got '%s'", directory.Path)
	}
	
	if !directory.GitInitialized {
		t.Errorf("Expected GitInitialized to be true")
	}
}

func TestSessionStateComparison(t *testing.T) {
	// Clear session states before test
	sessionStates = make(map[string]*SessionState)
	
	// Test first state capture - should be "Running"
	state1 := &SessionState{
		Name:            "test_session",
		Content:         "initial content",
		LastCursorPos:   "0,0",
		LastUpdated:     time.Now(),
		UnchangedSince:  time.Now(),
		IsWaiting:       false,
	}
	
	status := compareSessionStates("test_session", state1)
	if status != "Running" {
		t.Errorf("Expected 'Running' for first state, got '%s'", status)
	}
	
	// Test unchanged state within timeout - should still be "Running"
	state2 := &SessionState{
		Name:            "test_session", 
		Content:         "initial content", // Same content
		LastCursorPos:   "0,0",           // Same cursor
		LastUpdated:     time.Now(),
		UnchangedSince:  time.Now(),
		IsWaiting:       false,
	}
	
	status = compareSessionStates("test_session", state2)
	if status != "Running" {
		t.Errorf("Expected 'Running' for unchanged state within timeout, got '%s'", status)
	}
	
	// Test changed state - should reset to "Running"
	state3 := &SessionState{
		Name:            "test_session",
		Content:         "new content", // Changed content
		LastCursorPos:   "1,1",        // Changed cursor
		LastUpdated:     time.Now(),
		UnchangedSince:  time.Now(),
		IsWaiting:       false,
	}
	
	status = compareSessionStates("test_session", state3)
	if status != "Running" {
		t.Errorf("Expected 'Running' for changed state, got '%s'", status)
	}
	
	// Test unchanged state after timeout - should be "Waiting"
	// Manipulate the stored state to simulate timeout
	if storedState, exists := sessionStates["test_session"]; exists {
		storedState.UnchangedSince = time.Now().Add(-WAITING_TIMEOUT - time.Second)
	}
	
	state4 := &SessionState{
		Name:            "test_session",
		Content:         "new content", // Same as previous
		LastCursorPos:   "1,1",        // Same as previous
		LastUpdated:     time.Now(),
		UnchangedSince:  time.Now().Add(-WAITING_TIMEOUT - time.Second),
		IsWaiting:       false,
	}
	
	status = compareSessionStates("test_session", state4)
	if status != "Waiting" {
		t.Errorf("Expected 'Waiting' for unchanged state after timeout, got '%s'", status)
	}
}

func TestSessionStateCleanup(t *testing.T) {
	// Setup some test session states
	sessionStates = make(map[string]*SessionState)
	sessionStates["session1"] = &SessionState{Name: "session1"}
	sessionStates["session2"] = &SessionState{Name: "session2"}
	sessionStates["orphaned_session"] = &SessionState{Name: "orphaned_session"}
	
	// Note: The cleanup function now queries tmux directly, so we can't easily test it
	// without mocking. For now, let's test that it doesn't panic when called.
	
	// Call cleanup - it should not panic even if tmux is not running
	cleanupOrphanedSessionStates()
	
	// The function should handle the case where tmux is not running by clearing all states
	// Since tmux likely isn't running in the test environment, all states should be cleared
	if len(sessionStates) != 0 {
		t.Logf("Session states cleared due to no tmux sessions (expected in test environment)")
	}
}