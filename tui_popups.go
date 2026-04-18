package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Types ──────────────────────────────────────────────────────────────────

type planeIssue struct {
	ID              string   `json:"id"`
	Title           string   `json:"name"`
	Priority        string   `json:"priority"`
	SequenceID      int      `json:"sequence_id"`
	DescriptionHTML string   `json:"description_html"`
	Assignees       []string `json:"assignees"`
	Labels          []string `json:"labels"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
	StateGroup      string
	Identifier      string // e.g. "SWM-42" — built from project prefix + sequence_id
}

type icingaProblem struct {
	Host          string
	Service       string
	State         int // 1=warning, 2=critical, 3=unknown
	Output        string
	FullOutput    string // untruncated plugin output
	LastCheck     time.Time
	Duration      time.Duration
	CheckAttempt  int
	MaxAttempts   int
	Acknowledged  bool
	AckAuthor     string
	AckComment    string
	InDowntime    bool
	ObjectName    string // full object name for API calls (host!service)
}

// ─── Messages ───────────────────────────────────────────────────────────────

type planeIssuesMsg struct {
	reqID  uint64
	issues []planeIssue
}

type icingaProblemsMsg struct {
	reqID    uint64
	problems []icingaProblem
}

type auditEventsMsg struct {
	events []ManagedSessionEvent
}

type auditScrollbackMsg struct {
	sessionID string
	content   string
}

type popupErrMsg struct {
	reqID  uint64
	source string
	text   string
}

type popupActionDoneMsg struct {
	flash string
}

// ─── Plane config helper ────────────────────────────────────────────────────

type planeConfig struct {
	apiURL, apiKey, workspace, projectID string
}

func getPlaneConfig(api swarmClient) (planeConfig, error) {
	var c planeConfig
	if api != nil {
		c.apiURL, _ = api.getConfig("plane.api_url")
		c.apiKey, _ = api.getConfig("plane.api_key")
		c.workspace, _ = api.getConfig("plane.workspace")
		c.projectID, _ = api.getConfig("plane.project_id")
		if c.workspace == "" {
			c.workspace = "thomkernet"
		}
	} else if globalConfigService != nil {
		c.apiURL = globalConfigService.GetString("plane.api_url", "")
		c.apiKey = globalConfigService.GetString("plane.api_key", "")
		c.workspace = globalConfigService.GetString("plane.workspace", "thomkernet")
		c.projectID = globalConfigService.GetString("plane.project_id", "")
	} else {
		return c, fmt.Errorf("config service not initialized")
	}
	if c.apiURL == "" || c.apiKey == "" || c.projectID == "" {
		return c, fmt.Errorf("Plane not configured (set plane.api_url, plane.api_key, plane.project_id)")
	}
	return c, nil
}

// ─── Icinga config helper ───────────────────────────────────────────────────

type icingaConfig struct {
	apiURL, apiUser, apiPass string
}

func getIcingaConfig(api swarmClient) (icingaConfig, error) {
	var c icingaConfig
	if api != nil {
		c.apiURL, _ = api.getConfig("icinga.api_url")
		c.apiUser, _ = api.getConfig("icinga.api_user")
		c.apiPass, _ = api.getConfig("icinga.api_pass")
	} else if globalConfigService != nil {
		c.apiURL = globalConfigService.GetString("icinga.api_url", "")
		c.apiUser = globalConfigService.GetString("icinga.api_user", "")
		c.apiPass = globalConfigService.GetString("icinga.api_pass", "")
	} else {
		return c, fmt.Errorf("config service not initialized")
	}
	if c.apiURL == "" || c.apiUser == "" || c.apiPass == "" {
		return c, fmt.Errorf("Icinga not configured (set icinga.api_url, icinga.api_user, icinga.api_pass)")
	}
	return c, nil
}

func icingaHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

// ─── Data fetching ──────────────────────────────────────────────────────────

func fetchPlaneIssues(reqID uint64, api swarmClient) tea.Cmd {
	return func() tea.Msg {
		cfg, err := getPlaneConfig(api)
		if err != nil {
			return popupErrMsg{reqID, "plane", err.Error()}
		}

		client := &http.Client{Timeout: 15 * time.Second}
		baseURL := strings.TrimRight(cfg.apiURL, "/")

		// Fetch project prefix and all state groups in parallel.
		type groupResult struct {
			group  string
			issues []planeIssue
			err    error
		}

		prefixCh := make(chan string, 1)
		go func() {
			projURL := fmt.Sprintf("%s/api/v1/workspaces/%s/projects/%s/", baseURL, cfg.workspace, cfg.projectID)
			req, err := http.NewRequest("GET", projURL, nil)
			if err != nil {
				prefixCh <- ""
				return
			}
			req.Header.Set("X-API-Key", cfg.apiKey)
			resp, err := client.Do(req)
			if err != nil {
				prefixCh <- ""
				return
			}
			defer resp.Body.Close()
			var proj struct {
				Identifier string `json:"identifier"`
			}
			json.NewDecoder(resp.Body).Decode(&proj)
			prefixCh <- proj.Identifier
		}()

		groups := []string{"backlog", "unstarted", "started"}
		resultCh := make(chan groupResult, len(groups))
		for _, group := range groups {
			g := group
			go func() {
				url := fmt.Sprintf("%s/api/v1/workspaces/%s/projects/%s/issues/?state_group=%s&per_page=50&expand=assignees,labels",
					baseURL, cfg.workspace, cfg.projectID, g)
				req, err := http.NewRequest("GET", url, nil)
				if err != nil {
					resultCh <- groupResult{g, nil, err}
					return
				}
				req.Header.Set("X-API-Key", cfg.apiKey)
				req.Header.Set("Accept", "application/json")
				resp, err := client.Do(req)
				if err != nil {
					resultCh <- groupResult{g, nil, err}
					return
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					resultCh <- groupResult{g, nil, nil}
					return
				}
				var parsed struct {
					Results []planeIssue `json:"results"`
				}
				if json.Unmarshal(body, &parsed) != nil {
					resultCh <- groupResult{g, nil, nil}
					return
				}
				for i := range parsed.Results {
					parsed.Results[i].StateGroup = g
				}
				resultCh <- groupResult{g, parsed.Results, nil}
			}()
		}

		projectPrefix := <-prefixCh

		// Collect group results in stable order.
		byGroup := make(map[string][]planeIssue, len(groups))
		var firstErr error
		for range groups {
			r := <-resultCh
			if r.err != nil && firstErr == nil {
				firstErr = r.err
			}
			byGroup[r.group] = r.issues
		}
		if firstErr != nil && len(byGroup) == 0 {
			return popupErrMsg{reqID, "plane", fmt.Sprintf("HTTP error: %v", firstErr)}
		}

		var allIssues []planeIssue
		for _, g := range groups {
			for _, iss := range byGroup[g] {
				if projectPrefix != "" {
					iss.Identifier = fmt.Sprintf("%s-%d", projectPrefix, iss.SequenceID)
				}
				allIssues = append(allIssues, iss)
			}
		}

		return planeIssuesMsg{reqID, allIssues}
	}
}

func fetchIcingaProblems(reqID uint64, api swarmClient) tea.Cmd {
	return func() tea.Msg {
		cfg, err := getIcingaConfig(api)
		if err != nil {
			return popupErrMsg{reqID, "icinga", err.Error()}
		}

		url := fmt.Sprintf("%s/v1/objects/services?attrs=display_name&attrs=state&attrs=last_check_result&attrs=host_name&attrs=last_check&attrs=last_state_change&attrs=check_attempt&attrs=max_check_attempts&attrs=acknowledgement&attrs=acknowledgement_last_change&attrs=downtime_depth&attrs=name&filter=service.state!=0",
			strings.TrimRight(cfg.apiURL, "/"))

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return popupErrMsg{reqID, "icinga", fmt.Sprintf("request error: %v", err)}
		}
		req.SetBasicAuth(cfg.apiUser, cfg.apiPass)
		req.Header.Set("Accept", "application/json")

		resp, err := icingaHTTPClient().Do(req)
		if err != nil {
			return popupErrMsg{reqID, "icinga", fmt.Sprintf("HTTP error: %v", err)}
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			return popupErrMsg{reqID, "icinga", fmt.Sprintf("HTTP %d", resp.StatusCode)}
		}

		var parsed struct {
			Results []struct {
				Name  string `json:"name"`
				Attrs struct {
					DisplayName     string  `json:"display_name"`
					State           float64 `json:"state"`
					HostName        string  `json:"host_name"`
					LastCheckResult struct {
						Output string `json:"output"`
					} `json:"last_check_result"`
					LastCheck          float64 `json:"last_check"`
					LastStateChange    float64 `json:"last_state_change"`
					CheckAttempt       float64 `json:"check_attempt"`
					MaxCheckAttempts   float64 `json:"max_check_attempts"`
					Acknowledgement    float64 `json:"acknowledgement"`
					DowntimeDepth      float64 `json:"downtime_depth"`
				} `json:"attrs"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return popupErrMsg{reqID, "icinga", fmt.Sprintf("parse error: %v", err)}
		}

		now := time.Now()
		var problems []icingaProblem
		for _, r := range parsed.Results {
			output := r.Attrs.LastCheckResult.Output
			shortOutput := output
			if len(shortOutput) > 120 {
				shortOutput = shortOutput[:117] + "..."
			}

			var lastCheck time.Time
			if r.Attrs.LastCheck > 0 {
				lastCheck = time.Unix(int64(r.Attrs.LastCheck), 0)
			}
			var duration time.Duration
			if r.Attrs.LastStateChange > 0 {
				duration = now.Sub(time.Unix(int64(r.Attrs.LastStateChange), 0))
			}

			problems = append(problems, icingaProblem{
				Host:         r.Attrs.HostName,
				Service:      r.Attrs.DisplayName,
				State:        int(r.Attrs.State),
				Output:       shortOutput,
				FullOutput:   output,
				LastCheck:    lastCheck,
				Duration:     duration,
				CheckAttempt: int(r.Attrs.CheckAttempt),
				MaxAttempts:  int(r.Attrs.MaxCheckAttempts),
				Acknowledged: r.Attrs.Acknowledgement > 0,
				InDowntime:   r.Attrs.DowntimeDepth > 0,
				ObjectName:   r.Name,
			})
		}

		return icingaProblemsMsg{reqID, problems}
	}
}

// ─── Write actions ──────────────────────────────────────────────────────────

// planeUpdateIssue updates a Plane issue's state or assignees.
func planeUpdateIssue(api swarmClient, issueID string, updates map[string]interface{}) tea.Cmd {
	return func() tea.Msg {
		cfg, err := getPlaneConfig(api)
		if err != nil {
			return popupActionDoneMsg{flash: "Error: " + err.Error()}
		}

		url := fmt.Sprintf("%s/api/v1/workspaces/%s/projects/%s/issues/%s/",
			strings.TrimRight(cfg.apiURL, "/"), cfg.workspace, cfg.projectID, issueID)

		body, _ := json.Marshal(updates)
		req, err := http.NewRequest("PATCH", url, bytes.NewReader(body))
		if err != nil {
			return popupActionDoneMsg{flash: "Error: " + err.Error()}
		}
		req.Header.Set("X-API-Key", cfg.apiKey)
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return popupActionDoneMsg{flash: "Error: " + err.Error()}
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 300 {
			respBody, _ := io.ReadAll(resp.Body)
			return popupActionDoneMsg{flash: fmt.Sprintf("Plane %d: %s", resp.StatusCode, string(respBody))}
		}
		return popupActionDoneMsg{flash: "✓ Issue updated"}
	}
}

// planeStatesMsg carries async-fetched Plane state IDs.
type planeStatesMsg struct {
	states map[string]string
}

// fetchPlaneStates returns a tea.Cmd that fetches state IDs asynchronously.
func fetchPlaneStates(api swarmClient) tea.Cmd {
	return func() tea.Msg {
		return planeStatesMsg{states: planeGetStates(api)}
	}
}

// planeGetStates fetches the state IDs for a project to enable state transitions.
func planeGetStates(api swarmClient) map[string]string {
	cfg, err := getPlaneConfig(api)
	if err != nil {
		return nil
	}

	url := fmt.Sprintf("%s/api/v1/workspaces/%s/projects/%s/states/",
		strings.TrimRight(cfg.apiURL, "/"), cfg.workspace, cfg.projectID)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("X-API-Key", cfg.apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var parsed struct {
		Results []struct {
			ID    string `json:"id"`
			Group string `json:"group"`
			Name  string `json:"name"`
		} `json:"results"`
	}
	body, _ := io.ReadAll(resp.Body)
	json.Unmarshal(body, &parsed)

	// Map group name to first matching state ID
	states := make(map[string]string)
	for _, s := range parsed.Results {
		if _, exists := states[s.Group]; !exists {
			states[s.Group] = s.ID
		}
	}
	return states
}

// icingaAcknowledge acknowledges an Icinga service problem.
func icingaAcknowledge(api swarmClient, objectName, comment string) tea.Cmd {
	return func() tea.Msg {
		cfg, err := getIcingaConfig(api)
		if err != nil {
			return popupActionDoneMsg{flash: "Error: " + err.Error()}
		}

		url := fmt.Sprintf("%s/v1/actions/acknowledge-problem",
			strings.TrimRight(cfg.apiURL, "/"))

		payload := map[string]interface{}{
			"type":    "Service",
			"filter":  fmt.Sprintf(`service.__name==%q`, objectName),
			"author":  "SwarmOps TUI",
			"comment": comment,
			"sticky":  true,
		}
		body, _ := json.Marshal(payload)
		req, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			return popupActionDoneMsg{flash: "Error: " + err.Error()}
		}
		req.SetBasicAuth(cfg.apiUser, cfg.apiPass)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := icingaHTTPClient().Do(req)
		if err != nil {
			return popupActionDoneMsg{flash: "Ack error: " + err.Error()}
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 300 {
			respBody, _ := io.ReadAll(resp.Body)
			return popupActionDoneMsg{flash: fmt.Sprintf("Ack failed %d: %s", resp.StatusCode, string(respBody))}
		}
		return popupActionDoneMsg{flash: "✓ Acknowledged"}
	}
}

// icingaScheduleDowntime schedules a downtime for an Icinga service.
func icingaScheduleDowntime(api swarmClient, objectName string, duration time.Duration, comment string) tea.Cmd {
	return func() tea.Msg {
		cfg, err := getIcingaConfig(api)
		if err != nil {
			return popupActionDoneMsg{flash: "Error: " + err.Error()}
		}

		now := time.Now()
		url := fmt.Sprintf("%s/v1/actions/schedule-downtime",
			strings.TrimRight(cfg.apiURL, "/"))

		payload := map[string]interface{}{
			"type":       "Service",
			"filter":     fmt.Sprintf(`service.__name==%q`, objectName),
			"author":     "SwarmOps TUI",
			"comment":    comment,
			"start_time": now.Unix(),
			"end_time":   now.Add(duration).Unix(),
			"fixed":      true,
		}
		body, _ := json.Marshal(payload)
		req, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			return popupActionDoneMsg{flash: "Error: " + err.Error()}
		}
		req.SetBasicAuth(cfg.apiUser, cfg.apiPass)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := icingaHTTPClient().Do(req)
		if err != nil {
			return popupActionDoneMsg{flash: "Downtime error: " + err.Error()}
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 300 {
			respBody, _ := io.ReadAll(resp.Body)
			return popupActionDoneMsg{flash: fmt.Sprintf("Downtime failed %d: %s", resp.StatusCode, string(respBody))}
		}
		return popupActionDoneMsg{flash: fmt.Sprintf("✓ Downtime scheduled (%s)", formatDuration(duration))}
	}
}

// ─── Styles ─────────────────────────────────────────────────────────────────

var (
	popupTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#15a8a8"))
	detailLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#15a8a8"))
	stateColors     = map[int]lipgloss.Style{
		1: lipgloss.NewStyle().Foreground(lipgloss.Color("#ffcc00")), // warning/yellow
		2: lipgloss.NewStyle().Foreground(lipgloss.Color("#ff3333")), // critical/red
		3: lipgloss.NewStyle().Foreground(lipgloss.Color("#cc66ff")), // unknown/purple
	}
	priorityIcons = map[string]string{
		"urgent": "!!", "high": "! ", "medium": "- ", "low": "  ", "none": "  ",
	}
)

func icingaStateLabel(state int) string {
	switch state {
	case 1:
		return "WARNING "
	case 2:
		return "CRITICAL"
	case 3:
		return "UNKNOWN "
	default:
		return "OK      "
	}
}

// ─── Filter & Sort ──────────────────────────────────────────────────────────

var planeSortLabels = []string{"default", "priority", "state", "name"}
var icingaSortLabels = []string{"default", "severity", "host", "service"}

var priorityOrder = map[string]int{
	"urgent": 0, "high": 1, "medium": 2, "low": 3, "none": 4,
}

var stateGroupOrder = map[string]int{
	"started": 0, "unstarted": 1, "backlog": 2,
}

// Plane triage presets
var planeTriageLabels = []string{"all", "started", "high+urgent", "backlog"}

func filteredPlaneIssues(m tuiModel) []planeIssue {
	if m.planeIssues == nil {
		return nil
	}

	// Apply triage preset filter first
	var base []planeIssue
	switch m.popupTriageMode {
	case 1: // started only
		for _, issue := range m.planeIssues {
			if issue.StateGroup == "started" {
				base = append(base, issue)
			}
		}
	case 2: // high + urgent priority
		for _, issue := range m.planeIssues {
			if issue.Priority == "high" || issue.Priority == "urgent" {
				base = append(base, issue)
			}
		}
	case 3: // backlog only
		for _, issue := range m.planeIssues {
			if issue.StateGroup == "backlog" {
				base = append(base, issue)
			}
		}
	default: // 0 = all
		base = m.planeIssues
	}

	query := strings.ToLower(strings.TrimSpace(m.popupFilter.Value()))
	if query == "" {
		return sortPlaneIssues(base, m.popupSortMode)
	}
	var out []planeIssue
	for _, issue := range base {
		if strings.Contains(strings.ToLower(issue.Title), query) ||
			strings.Contains(strings.ToLower(issue.StateGroup), query) ||
			strings.Contains(strings.ToLower(issue.Priority), query) ||
			strings.Contains(strings.ToLower(issue.Identifier), query) {
			out = append(out, issue)
		}
	}
	return sortPlaneIssues(out, m.popupSortMode)
}

func sortPlaneIssues(issues []planeIssue, mode int) []planeIssue {
	if mode == 0 || len(issues) <= 1 {
		return issues
	}
	sorted := make([]planeIssue, len(issues))
	copy(sorted, issues)
	switch mode {
	case 1: // priority
		sort.SliceStable(sorted, func(i, j int) bool {
			return priorityOrder[sorted[i].Priority] < priorityOrder[sorted[j].Priority]
		})
	case 2: // state
		sort.SliceStable(sorted, func(i, j int) bool {
			return stateGroupOrder[sorted[i].StateGroup] < stateGroupOrder[sorted[j].StateGroup]
		})
	case 3: // name
		sort.SliceStable(sorted, func(i, j int) bool {
			return strings.ToLower(sorted[i].Title) < strings.ToLower(sorted[j].Title)
		})
	}
	return sorted
}

func filteredIcingaProblems(m tuiModel) []icingaProblem {
	if m.icingaProblems == nil {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(m.popupFilter.Value()))
	if query == "" {
		return sortIcingaProblems(m.icingaProblems, m.popupSortMode)
	}
	var out []icingaProblem
	for _, p := range m.icingaProblems {
		if strings.Contains(strings.ToLower(p.Host), query) ||
			strings.Contains(strings.ToLower(p.Service), query) ||
			strings.Contains(strings.ToLower(p.Output), query) {
			out = append(out, p)
		}
	}
	return sortIcingaProblems(out, m.popupSortMode)
}

func sortIcingaProblems(problems []icingaProblem, mode int) []icingaProblem {
	if mode == 0 || len(problems) <= 1 {
		return problems
	}
	sorted := make([]icingaProblem, len(problems))
	copy(sorted, problems)
	switch mode {
	case 1: // severity (critical first)
		sort.SliceStable(sorted, func(i, j int) bool {
			return sorted[i].State > sorted[j].State
		})
	case 2: // host
		sort.SliceStable(sorted, func(i, j int) bool {
			return strings.ToLower(sorted[i].Host) < strings.ToLower(sorted[j].Host)
		})
	case 3: // service
		sort.SliceStable(sorted, func(i, j int) bool {
			return strings.ToLower(sorted[i].Service) < strings.ToLower(sorted[j].Service)
		})
	}
	return sorted
}

// groupIcingaByHost groups problems by host with counts.
func groupIcingaByHost(problems []icingaProblem) []icingaHostGroup {
	hostMap := make(map[string]*icingaHostGroup)
	var order []string
	for _, p := range problems {
		g, ok := hostMap[p.Host]
		if !ok {
			g = &icingaHostGroup{Host: p.Host}
			hostMap[p.Host] = g
			order = append(order, p.Host)
		}
		g.Problems = append(g.Problems, p)
		if p.State == 2 {
			g.CritCount++
		} else if p.State == 1 {
			g.WarnCount++
		}
	}
	var groups []icingaHostGroup
	for _, host := range order {
		groups = append(groups, *hostMap[host])
	}
	return groups
}

type icingaHostGroup struct {
	Host      string
	Problems  []icingaProblem
	CritCount int
	WarnCount int
	Expanded  bool
}

// ─── Prompt generation (richer payloads) ────────────────────────────────────

func planeIssuePrompt(issue planeIssue) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Work on Plane issue %s: %s\n", issue.Identifier, issue.Title))
	sb.WriteString(fmt.Sprintf("Priority: %s | State: %s\n", issue.Priority, issue.StateGroup))
	if issue.DescriptionHTML != "" {
		// Strip basic HTML tags for plain text prompt
		desc := stripHTMLTags(issue.DescriptionHTML)
		if len(desc) > 500 {
			desc = desc[:497] + "..."
		}
		sb.WriteString(fmt.Sprintf("Description: %s\n", desc))
	}
	return sb.String()
}

func icingaProblemPrompt(problem icingaProblem) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Investigate Icinga alert: %s on %s\n", problem.Service, problem.Host))
	sb.WriteString(fmt.Sprintf("State: %s (for %s)\n", icingaStateLabel(problem.State), formatDuration(problem.Duration)))
	sb.WriteString(fmt.Sprintf("Output: %s\n", problem.FullOutput))
	if problem.Acknowledged {
		sb.WriteString(fmt.Sprintf("Already acknowledged by: %s\n", problem.AckAuthor))
	}
	return sb.String()
}

// ─── Rendering: Plane ───────────────────────────────────────────────────────

func renderPlanePopup(m tuiModel) string {
	listWidth := m.w / 2
	if listWidth < 40 {
		listWidth = 40
	}
	detailWidth := m.w - listWidth - 3 // 3 for separator
	if detailWidth < 20 {
		detailWidth = 20
	}

	// ── Left: issue list ──
	var left strings.Builder

	title := "Plane Issues"
	sortLabel := planeSortLabels[m.popupSortMode%len(planeSortLabels)]
	if m.popupSortMode > 0 {
		title += " [" + sortLabel + "]"
	}
	triageLabel := planeTriageLabels[m.popupTriageMode%len(planeTriageLabels)]
	if m.popupTriageMode > 0 {
		title += " {" + triageLabel + "}"
	}
	left.WriteString(popupTitleStyle.Render(title) + "\n")

	if m.popupFilterActive || m.popupFilter.Value() != "" {
		left.WriteString(" / " + m.popupFilter.View() + "\n")
	}
	left.WriteString("\n")

	if m.popupErr != "" {
		left.WriteString(dimStyle.Render(" Error: "+m.popupErr) + "\n")
	} else if m.planeIssues == nil {
		left.WriteString(dimStyle.Render(" Loading...") + "\n")
	} else {
		filtered := filteredPlaneIssues(m)
		if len(filtered) == 0 {
			left.WriteString(dimStyle.Render(" No issues found.") + "\n")
		} else {
			maxLines := m.h - 6
			if maxLines < 5 {
				maxLines = 5
			}
			start := 0
			if m.popupCursor >= maxLines {
				start = m.popupCursor - maxLines + 1
			}
			end := start + maxLines
			if end > len(filtered) {
				end = len(filtered)
			}

			for i := start; i < end; i++ {
				issue := filtered[i]
				icon := priorityIcons[issue.Priority]
				if icon == "" {
					icon = "  "
				}
				id := issue.Identifier
				if id == "" {
					id = fmt.Sprintf("#%d", issue.SequenceID)
				}

				stateTag := fmt.Sprintf("%-8s", issue.StateGroup)
				maxTitle := listWidth - 25
				if maxTitle < 10 {
					maxTitle = 10
				}
				issueTitle := issue.Title
				if len(issueTitle) > maxTitle {
					issueTitle = issueTitle[:maxTitle-3] + "..."
				}

				line := fmt.Sprintf(" %s %-7s [%s] %s", icon, id, stateTag, issueTitle)
				if i == m.popupCursor {
					line = selectedStyle.Render(line)
				}
				left.WriteString(line + "\n")
			}

			if len(filtered) > maxLines {
				left.WriteString(dimStyle.Render(fmt.Sprintf(" (%d/%d shown)", min(maxLines, len(filtered)), len(filtered))) + "\n")
			}
		}
	}

	// ── Right: detail pane ──
	var right strings.Builder
	filtered := filteredPlaneIssues(m)
	if m.popupCursor < len(filtered) {
		issue := filtered[m.popupCursor]
		right.WriteString(detailLabelStyle.Render(issue.Identifier+" — "+issue.Title) + "\n\n")
		right.WriteString(fmt.Sprintf(" State:    %s\n", issue.StateGroup))
		right.WriteString(fmt.Sprintf(" Priority: %s\n", issue.Priority))
		if len(issue.Assignees) > 0 {
			right.WriteString(fmt.Sprintf(" Assigned: %s\n", strings.Join(issue.Assignees, ", ")))
		} else {
			right.WriteString(" Assigned: (none)\n")
		}
		if len(issue.Labels) > 0 {
			right.WriteString(fmt.Sprintf(" Labels:   %s\n", strings.Join(issue.Labels, ", ")))
		}
		if issue.UpdatedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, issue.UpdatedAt); err == nil {
				right.WriteString(fmt.Sprintf(" Updated:  %s\n", t.Format("2006-01-02 15:04")))
			}
		}
		right.WriteString("\n")

		if issue.DescriptionHTML != "" {
			desc := stripHTMLTags(issue.DescriptionHTML)
			lines := strings.Split(desc, "\n")
			maxDescLines := m.h - 12
			if maxDescLines < 3 {
				maxDescLines = 3
			}
			for i, line := range lines {
				if i >= maxDescLines {
					right.WriteString(dimStyle.Render(" ...") + "\n")
					break
				}
				if len(line) > detailWidth-2 {
					line = line[:detailWidth-5] + "..."
				}
				right.WriteString(" " + line + "\n")
			}
		} else {
			right.WriteString(dimStyle.Render(" (no description)") + "\n")
		}
	}

	// ── Compose split pane ──
	leftStr := lipgloss.NewStyle().Width(listWidth).Render(left.String())
	rightStr := lipgloss.NewStyle().Width(detailWidth).Render(right.String())

	content := lipgloss.JoinHorizontal(lipgloss.Top, leftStr, " ", rightStr)

	help := dimStyle.Render(" ↑↓ nav │ / filter │ s sort │ 1-3 triage │ p progress │ d done │ Enter dispatch │ r refresh │ q close")
	return content + "\n" + help
}

// ─── Rendering: Icinga ──────────────────────────────────────────────────────

func renderIcingaPopup(m tuiModel) string {
	listWidth := m.w / 2
	if listWidth < 40 {
		listWidth = 40
	}
	detailWidth := m.w - listWidth - 3
	if detailWidth < 20 {
		detailWidth = 20
	}

	// ── Left: problem list ──
	var left strings.Builder

	title := "Icinga Alerts"
	sortLabel := icingaSortLabels[m.popupSortMode%len(icingaSortLabels)]
	if m.popupSortMode > 0 {
		title += " [" + sortLabel + "]"
	}
	if m.icingaGroupByHost {
		title += " {by host}"
	}
	left.WriteString(popupTitleStyle.Render(title) + "\n")

	if m.popupFilterActive || m.popupFilter.Value() != "" {
		left.WriteString(" / " + m.popupFilter.View() + "\n")
	}
	left.WriteString("\n")

	if m.popupErr != "" {
		left.WriteString(dimStyle.Render(" Error: "+m.popupErr) + "\n")
	} else if m.icingaProblems == nil {
		left.WriteString(dimStyle.Render(" Loading...") + "\n")
	} else {
		filtered := filteredIcingaProblems(m)
		if len(filtered) == 0 {
			left.WriteString(dimStyle.Render(" No active problems. All clear!") + "\n")
		} else if m.icingaGroupByHost {
			groups := groupIcingaByHost(filtered)
			idx := 0
			for _, g := range groups {
				summary := fmt.Sprintf(" %-20s (%d svc", g.Host, len(g.Problems))
				if g.CritCount > 0 {
					summary += fmt.Sprintf(", %d crit", g.CritCount)
				}
				summary += ")"
				if idx == m.popupCursor {
					left.WriteString(selectedStyle.Render(summary) + "\n")
				} else {
					left.WriteString(summary + "\n")
				}
				for _, p := range g.Problems {
					idx++
					stateStyle := stateColors[p.State]
					if stateStyle.GetForeground() == (lipgloss.NoColor{}) {
						stateStyle = dimStyle
					}
					line := fmt.Sprintf("   %s %s", icingaStateLabel(p.State), p.Service)
					if p.Acknowledged {
						line += " [acked]"
					}
					if idx == m.popupCursor {
						line = selectedStyle.Render(line)
					} else {
						line = stateStyle.Render(line)
					}
					left.WriteString(line + "\n")
				}
				idx++
			}
		} else {
			maxLines := m.h - 6
			if maxLines < 5 {
				maxLines = 5
			}
			start := 0
			if m.popupCursor >= maxLines {
				start = m.popupCursor - maxLines + 1
			}
			end := start + maxLines
			if end > len(filtered) {
				end = len(filtered)
			}
			for i := start; i < end; i++ {
				p := filtered[i]
				stateStyle := stateColors[p.State]
				if stateStyle.GetForeground() == (lipgloss.NoColor{}) {
					stateStyle = dimStyle
				}
				label := icingaStateLabel(p.State)
				dur := formatDuration(p.Duration)
				line := fmt.Sprintf(" %s %s / %s (%s)", label, p.Host, p.Service, dur)
				if p.Acknowledged {
					line += " [acked]"
				}
				if p.InDowntime {
					line += " [dt]"
				}
				maxLine := listWidth - 2
				if len(line) > maxLine {
					line = line[:maxLine-3] + "..."
				}
				if i == m.popupCursor {
					line = selectedStyle.Render(line)
				} else {
					line = stateStyle.Render(line)
				}
				left.WriteString(line + "\n")
			}
			if len(filtered) > maxLines {
				left.WriteString(dimStyle.Render(fmt.Sprintf("  … %d more (↑↓ to scroll)", len(filtered)-maxLines)) + "\n")
			}
		}
	}

	// ── Right: detail pane ──
	var right strings.Builder
	filtered := filteredIcingaProblems(m)
	if m.popupCursor < len(filtered) {
		p := filtered[m.popupCursor]
		stateStyle := stateColors[p.State]
		if stateStyle.GetForeground() == (lipgloss.NoColor{}) {
			stateStyle = dimStyle
		}

		right.WriteString(detailLabelStyle.Render(p.Host+" / "+p.Service) + "\n\n")
		right.WriteString(fmt.Sprintf(" State:    %s\n", stateStyle.Render(strings.TrimSpace(icingaStateLabel(p.State)))))
		right.WriteString(fmt.Sprintf(" Duration: %s\n", formatDuration(p.Duration)))
		right.WriteString(fmt.Sprintf(" Attempt:  %d/%d\n", p.CheckAttempt, p.MaxAttempts))
		if !p.LastCheck.IsZero() {
			right.WriteString(fmt.Sprintf(" Checked:  %s\n", p.LastCheck.Format("15:04:05")))
		}
		if p.Acknowledged {
			right.WriteString(fmt.Sprintf(" Acked:    yes (%s)\n", p.AckAuthor))
			if p.AckComment != "" {
				right.WriteString(fmt.Sprintf("           %s\n", p.AckComment))
			}
		}
		if p.InDowntime {
			right.WriteString(" Downtime: active\n")
		}
		right.WriteString("\n")

		// Full output
		right.WriteString(detailLabelStyle.Render("Output:") + "\n")
		output := p.FullOutput
		if output == "" {
			output = "(no output)"
		}
		// Word-wrap output to detail width
		lines := wrapText(output, detailWidth-2)
		maxLines := m.h - 14
		if maxLines < 3 {
			maxLines = 3
		}
		for i, line := range lines {
			if i >= maxLines {
				right.WriteString(dimStyle.Render(" ...") + "\n")
				break
			}
			right.WriteString(" " + line + "\n")
		}
	}

	// ── Compose split pane ──
	leftStr := lipgloss.NewStyle().Width(listWidth).Render(left.String())
	rightStr := lipgloss.NewStyle().Width(detailWidth).Render(right.String())
	content := lipgloss.JoinHorizontal(lipgloss.Top, leftStr, " ", rightStr)

	help := dimStyle.Render(" ↑↓ nav │ / filter │ s sort │ g group │ a ack │ d downtime │ Enter dispatch │ r refresh │ q close")
	return content + "\n" + help
}

// ─── Action picker ──────────────────────────────────────────────────────────

func renderActionPicker(m tuiModel) string {
	var sb strings.Builder

	target := m.actionTarget
	if len(target) > 60 {
		target = target[:57] + "..."
	}
	sb.WriteString(popupTitleStyle.Render("Act on: "+target))
	sb.WriteString("\n\n")

	if len(m.actionSessions) > 0 {
		sb.WriteString("  Send to existing session:\n")
		for i, s := range m.actionSessions {
			prefix := "    "
			if i == m.actionCursor {
				prefix = selectedStyle.Render("  > ")
			}
			sb.WriteString(prefix + s.label + " (" + s.status + ")\n")
		}
		sb.WriteString("\n")
	}

	newIdx := len(m.actionSessions)
	prefix := "    "
	if m.actionCursor == newIdx {
		prefix = selectedStyle.Render("  > ")
	}
	sb.WriteString("  Spawn new session:\n")
	sb.WriteString(prefix + "[New session with this task]\n")

	sb.WriteString("\n" + dimStyle.Render("  ↑↓ select | Enter confirm | Esc cancel"))
	return sb.String()
}

// renderDispatchContextPicker renders the context picker for the dispatch flow.
func renderDispatchContextPicker(m tuiModel) string {
	var sb strings.Builder

	target := m.actionTarget
	if len(target) > 60 {
		target = target[:57] + "..."
	}
	sb.WriteString(popupTitleStyle.Render("Dispatch: "+target))
	sb.WriteString("\n\n")
	sb.WriteString("  Add session context (reference material for Claude):\n\n")

	// "(none)" option
	label := "(none — dispatch without context)"
	if m.actionCtxCursor == 0 {
		label = selectedStyle.Render("  > " + label)
	} else {
		label = "    " + label
	}
	sb.WriteString(label + "\n")

	if m.contexts == nil {
		sb.WriteString(dimStyle.Render("    Loading contexts...") + "\n")
	} else {
		for i, c := range m.contexts {
			name := c.Name
			if len(name) > 40 {
				name = name[:37] + "..."
			}
			if i+1 == m.actionCtxCursor {
				sb.WriteString(selectedStyle.Render("  > "+name) + "\n")
			} else {
				sb.WriteString("    " + name + "\n")
			}
		}
	}

	sb.WriteString("\n" + dimStyle.Render("  ↑↓ select | Enter confirm | Esc skip (no context)"))
	return sb.String()
}

// submitFeedback creates a Plane issue in the SwarmOps feedback project.
func submitFeedback(kind, summary string, api swarmClient, tuiSnapshot string) {
	cfg, err := getPlaneConfig(api)
	if err != nil {
		log.Printf("feedback: %v", err)
		return
	}
	feedbackProjectID := ""
	if api != nil {
		feedbackProjectID, _ = api.getConfig("feedback.project_id")
	} else if globalConfigService != nil {
		feedbackProjectID = globalConfigService.GetString("feedback.project_id", "")
	}
	if feedbackProjectID == "" {
		log.Printf("feedback: feedback.project_id not configured")
		return
	}

	prefix := "[bug] "
	if kind == "feature" {
		prefix = "[feature] "
	}
	url := fmt.Sprintf("%s/api/v1/workspaces/%s/projects/%s/issues/",
		strings.TrimRight(cfg.apiURL, "/"), cfg.workspace, feedbackProjectID)
	issueData := map[string]string{"name": prefix + summary}
	if tuiSnapshot != "" {
		issueData["description_html"] = fmt.Sprintf("<p>%s</p><h3>TUI State at Report Time</h3><pre>%s</pre>",
			summary, tuiSnapshot)
	}
	body, _ := json.Marshal(issueData)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		log.Printf("feedback: request error: %v", err)
		return
	}
	req.Header.Set("X-API-Key", cfg.apiKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("feedback: HTTP error: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("feedback: Plane returned %d: %s", resp.StatusCode, string(respBody))
	}
}

// ─── Helpers ────────────────────────────────────────────────────────────────

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m > 0 {
			return fmt.Sprintf("%dh%dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	if hours > 0 {
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	return fmt.Sprintf("%dd", days)
}

func wrapText(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		for len(line) > width {
			// Find last space within width
			cut := strings.LastIndex(line[:width], " ")
			if cut <= 0 {
				cut = width
			}
			lines = append(lines, line[:cut])
			line = strings.TrimSpace(line[cut:])
		}
		lines = append(lines, line)
	}
	return lines
}

func stripHTMLTags(s string) string {
	var out strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			out.WriteRune(r)
		}
	}
	// Clean up excessive whitespace
	result := strings.TrimSpace(out.String())
	// Collapse multiple newlines
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}
	return result
}

// ─── Plane: close session issue (SWM-26) ────────────────────────────────────

// planeIssueRefRe matches identifiers like "SWM-42" anywhere in a string.
var planeIssueRefRe = regexp.MustCompile(`\b([A-Z]+-\d+)\b`)

// planeCloseSessionIssue finds the first "PROJECT-N" token in sessionName,
// looks it up in Plane, transitions it to the "completed" group state, and
// returns a popupActionDoneMsg flash.
func planeCloseSessionIssue(sessionName string, api swarmClient) tea.Cmd {
	return func() tea.Msg {
		m := planeIssueRefRe.FindStringSubmatch(sessionName)
		if m == nil {
			return popupActionDoneMsg{flash: "No issue ref found in session name (e.g. SWM-42)"}
		}
		identifier := m[1]

		cfg, err := getPlaneConfig(api)
		if err != nil {
			return popupActionDoneMsg{flash: "Plane not configured: " + err.Error()}
		}

		// Fetch states to find the "completed" state ID.
		states := planeGetStates(api)
		doneStateID, ok := states["completed"]
		if !ok {
			return popupActionDoneMsg{flash: "Could not find 'completed' state in Plane project"}
		}

		// Fetch issues to find the issue UUID matching identifier.
		issueURL := fmt.Sprintf("%s/api/v1/workspaces/%s/projects/%s/issues/?per_page=250",
			strings.TrimRight(cfg.apiURL, "/"), cfg.workspace, cfg.projectID)
		req, err := http.NewRequest("GET", issueURL, nil)
		if err != nil {
			return popupActionDoneMsg{flash: "Error: " + err.Error()}
		}
		req.Header.Set("X-API-Key", cfg.apiKey)
		req.Header.Set("Accept", "application/json")
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return popupActionDoneMsg{flash: "Error: " + err.Error()}
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		var parsed struct {
			Results []struct {
				ID         string `json:"id"`
				SequenceID int    `json:"sequence_id"`
			} `json:"results"`
		}
		json.Unmarshal(body, &parsed)

		// Match identifier suffix (e.g. "42" in "SWM-42").
		parts := strings.SplitN(identifier, "-", 2)
		if len(parts) != 2 {
			return popupActionDoneMsg{flash: "Could not parse issue ref: " + identifier}
		}
		var seqID int
		fmt.Sscanf(parts[1], "%d", &seqID)

		var issueID string
		for _, iss := range parsed.Results {
			if iss.SequenceID == seqID {
				issueID = iss.ID
				break
			}
		}
		if issueID == "" {
			return popupActionDoneMsg{flash: "Issue " + identifier + " not found in Plane project"}
		}

		// PATCH the issue to set state = completed.
		patchURL := fmt.Sprintf("%s/api/v1/workspaces/%s/projects/%s/issues/%s/",
			strings.TrimRight(cfg.apiURL, "/"), cfg.workspace, cfg.projectID, issueID)
		patchBody, _ := json.Marshal(map[string]string{"state": doneStateID})
		patchReq, err := http.NewRequest("PATCH", patchURL, bytes.NewReader(patchBody))
		if err != nil {
			return popupActionDoneMsg{flash: "Error: " + err.Error()}
		}
		patchReq.Header.Set("X-API-Key", cfg.apiKey)
		patchReq.Header.Set("Content-Type", "application/json")
		patchResp, err := client.Do(patchReq)
		if err != nil {
			return popupActionDoneMsg{flash: "Error: " + err.Error()}
		}
		defer patchResp.Body.Close()
		if patchResp.StatusCode >= 300 {
			respBody, _ := io.ReadAll(patchResp.Body)
			return popupActionDoneMsg{flash: fmt.Sprintf("Plane %d: %s", patchResp.StatusCode, string(respBody))}
		}
		return popupActionDoneMsg{flash: fmt.Sprintf("✓ %s marked done in Plane", identifier)}
	}
}

// ─── Audit log commands ──────────────────────────────────────────────────────

func fetchAuditEvents(api swarmClient) tea.Cmd {
	return func() tea.Msg {
		var events []ManagedSessionEvent
		var err error
		if api != nil {
			events, err = api.listAuditEvents(200)
		} else {
			events, err = listAuditEvents(context.Background(), 200)
		}
		if err != nil {
			return popupErrMsg{source: "audit", text: err.Error()}
		}
		return auditEventsMsg{events: events}
	}
}

func fetchAuditScrollback(sessionID string) tea.Cmd {
	return func() tea.Msg {
		content, _ := loadArchivedScrollback(sessionID)
		return auditScrollbackMsg{sessionID: sessionID, content: content}
	}
}

// ─── Audit popup renderer ────────────────────────────────────────────────────

func renderAuditPopup(m tuiModel) string {
	listWidth := m.w / 2
	if listWidth < 40 {
		listWidth = 40
	}
	detailWidth := m.w - listWidth - 3
	if detailWidth < 20 {
		detailWidth = 20
	}

	// ── Left: event list ──
	var left strings.Builder
	left.WriteString(dimStyle.Render("Session Audit Trail") + "\n")
	left.WriteString(strings.Repeat("─", listWidth) + "\n")

	if m.auditEvents == nil {
		left.WriteString(dimStyle.Render("Loading..."))
	} else if len(m.auditEvents) == 0 {
		left.WriteString(dimStyle.Render("No events recorded yet"))
	} else {
		for i, ev := range m.auditEvents {
			t := time.Unix(ev.Timestamp, 0).Format("01-02 15:04")
			icon := auditEventIcon(ev.EventType)
			label := fmt.Sprintf("%s %s  %s  %s", icon, t, ev.EventType, ev.Name)
			if len(label) > listWidth-2 {
				label = label[:listWidth-2]
			}
			if i == m.popupCursor {
				left.WriteString(selectedStyle.Render(label) + "\n")
			} else {
				left.WriteString(label + "\n")
			}
		}
	}

	// ── Right: scrollback ──
	var right strings.Builder
	right.WriteString(dimStyle.Render("Session Scrollback") + "\n")
	right.WriteString(strings.Repeat("─", detailWidth) + "\n")

	if m.auditScrollback == "" {
		if m.auditEvents != nil && len(m.auditEvents) > 0 {
			right.WriteString(dimStyle.Render("No scrollback saved for this session"))
		}
	} else {
		lines := strings.Split(m.auditScrollback, "\n")
		// Show last detailWidth-4 lines to fit the pane
		maxLines := m.h - 6
		if maxLines < 1 {
			maxLines = 10
		}
		if len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
		}
		for _, line := range lines {
			if len(line) > detailWidth {
				line = line[:detailWidth]
			}
			right.WriteString(line + "\n")
		}
	}

	sep := strings.Repeat("│\n", m.h-2)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		left.String(),
		dimStyle.Render(sep),
		right.String(),
	) + "\n" + dimStyle.Render("↑↓ navigate · Esc close")
}

func auditEventIcon(eventType string) string {
	switch eventType {
	case "created":
		return "✦"
	case "stopped":
		return "■"
	case "deleted":
		return "✕"
	case "renamed":
		return "✎"
	case "mission_set":
		return "◎"
	default:
		return "·"
	}
}
