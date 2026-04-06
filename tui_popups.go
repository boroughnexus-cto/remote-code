package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Types ──────────────────────────────────────────────────────────────────

type planeIssue struct {
	ID         string `json:"id"`
	Title      string `json:"name"`
	Priority   string `json:"priority"`
	SequenceID int    `json:"sequence_id"`
	StateGroup string
}

type icingaProblem struct {
	Host    string
	Service string
	State   int // 1=warning, 2=critical, 3=unknown
	Output  string
}

// ─── Messages ───────────────────────────────────────────────────────────────

type planeIssuesMsg []planeIssue
type icingaProblemsMsg []icingaProblem
type popupErrMsg struct{ source, text string }

// ─── Data fetching ──────────────────────────────────────────────────────────

func fetchPlaneIssues() tea.Cmd {
	return func() tea.Msg {
		if globalConfigService == nil {
			return popupErrMsg{"plane", "config service not initialized"}
		}

		apiURL := globalConfigService.GetString("plane.api_url", "")
		apiKey := globalConfigService.GetString("plane.api_key", "")
		workspace := globalConfigService.GetString("plane.workspace", "thomkernet")
		projectID := globalConfigService.GetString("plane.project_id", "")

		if apiURL == "" || apiKey == "" || projectID == "" {
			return popupErrMsg{"plane", "Plane not configured (set plane.api_url, plane.api_key, plane.project_id)"}
		}

		client := &http.Client{Timeout: 10 * time.Second}
		var allIssues []planeIssue

		for _, group := range []string{"backlog", "unstarted", "started"} {
			url := fmt.Sprintf("%s/api/v1/workspaces/%s/projects/%s/issues/?state_group=%s&per_page=50",
				strings.TrimRight(apiURL, "/"), workspace, projectID, group)

			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				continue
			}
			req.Header.Set("X-API-Key", apiKey)
			req.Header.Set("Accept", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				return popupErrMsg{"plane", fmt.Sprintf("HTTP error: %v", err)}
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode != 200 {
				continue
			}

			var parsed struct {
				Results []planeIssue `json:"results"`
			}
			if json.Unmarshal(body, &parsed) != nil {
				continue
			}
			for i := range parsed.Results {
				parsed.Results[i].StateGroup = group
			}
			allIssues = append(allIssues, parsed.Results...)
		}

		return planeIssuesMsg(allIssues)
	}
}

func fetchIcingaProblems() tea.Cmd {
	return func() tea.Msg {
		if globalConfigService == nil {
			return popupErrMsg{"icinga", "config service not initialized"}
		}

		apiURL := globalConfigService.GetString("icinga.api_url", "")
		apiUser := globalConfigService.GetString("icinga.api_user", "")
		apiPass := globalConfigService.GetString("icinga.api_pass", "")

		if apiURL == "" || apiUser == "" || apiPass == "" {
			return popupErrMsg{"icinga", "Icinga not configured (set icinga.api_url, icinga.api_user, icinga.api_pass)"}
		}

		url := fmt.Sprintf("%s/v1/objects/services?attrs=display_name&attrs=state&attrs=last_check_result&attrs=host_name&filter=service.state!=0",
			strings.TrimRight(apiURL, "/"))

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return popupErrMsg{"icinga", fmt.Sprintf("request error: %v", err)}
		}
		req.SetBasicAuth(apiUser, apiPass)
		req.Header.Set("Accept", "application/json")

		// Icinga uses self-signed certs
		client := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}

		resp, err := client.Do(req)
		if err != nil {
			return popupErrMsg{"icinga", fmt.Sprintf("HTTP error: %v", err)}
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			return popupErrMsg{"icinga", fmt.Sprintf("HTTP %d", resp.StatusCode)}
		}

		var parsed struct {
			Results []struct {
				Attrs struct {
					DisplayName     string  `json:"display_name"`
					State           float64 `json:"state"`
					HostName        string  `json:"host_name"`
					LastCheckResult struct {
						Output string `json:"output"`
					} `json:"last_check_result"`
				} `json:"attrs"`
			} `json:"results"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return popupErrMsg{"icinga", fmt.Sprintf("parse error: %v", err)}
		}

		var problems []icingaProblem
		for _, r := range parsed.Results {
			output := r.Attrs.LastCheckResult.Output
			if len(output) > 120 {
				output = output[:117] + "..."
			}
			problems = append(problems, icingaProblem{
				Host:    r.Attrs.HostName,
				Service: r.Attrs.DisplayName,
				State:   int(r.Attrs.State),
				Output:  output,
			})
		}

		return icingaProblemsMsg(problems)
	}
}

// ─── Rendering ──────────────────────────────────────────────────────────────

var (
	popupTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#15a8a8"))
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

// planeSortLabels are the sort mode names, indexed by popupSortMode.
var planeSortLabels = []string{"default", "priority", "state", "name"}

// icingaSortLabels are the sort mode names, indexed by popupSortMode.
var icingaSortLabels = []string{"default", "severity", "host", "service"}

var priorityOrder = map[string]int{
	"urgent": 0, "high": 1, "medium": 2, "low": 3, "none": 4,
}

var stateGroupOrder = map[string]int{
	"started": 0, "unstarted": 1, "backlog": 2,
}

func filteredPlaneIssues(m tuiModel) []planeIssue {
	if m.planeIssues == nil {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(m.popupFilter.Value()))
	if query == "" {
		return sortPlaneIssues(m.planeIssues, m.popupSortMode)
	}
	var out []planeIssue
	for _, issue := range m.planeIssues {
		if strings.Contains(strings.ToLower(issue.Title), query) ||
			strings.Contains(strings.ToLower(issue.StateGroup), query) ||
			strings.Contains(strings.ToLower(issue.Priority), query) {
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

// ─── Prompt generation ──────────────────────────────────────────────────────

func planeIssuePrompt(issue planeIssue) string {
	return fmt.Sprintf("Work on Plane issue: %s (priority: %s, state: %s)", issue.Title, issue.Priority, issue.StateGroup)
}

func icingaProblemPrompt(problem icingaProblem) string {
	return fmt.Sprintf("Investigate Icinga alert: %s on %s — %s", problem.Service, problem.Host, problem.Output)
}

// ─── Rendering ──────────────────────────────────────────────────────────────

func renderPlanePopup(m tuiModel) string {
	var sb strings.Builder

	title := "Plane Issues — Backlog & In Progress"
	sortLabel := planeSortLabels[m.popupSortMode%len(planeSortLabels)]
	if m.popupSortMode > 0 {
		title += "  [sorted: " + sortLabel + "]"
	}
	sb.WriteString(popupTitleStyle.Render(title))
	sb.WriteString("\n")

	if m.popupFilterActive || m.popupFilter.Value() != "" {
		sb.WriteString("  / " + m.popupFilter.View() + "\n")
	}
	sb.WriteString("\n")

	if m.popupErr != "" {
		sb.WriteString(dimStyle.Render("  Error: "+m.popupErr) + "\n")
	} else if m.planeIssues == nil {
		sb.WriteString(dimStyle.Render("  Loading...") + "\n")
	} else {
		filtered := filteredPlaneIssues(m)
		if len(filtered) == 0 {
			sb.WriteString(dimStyle.Render("  No issues found.") + "\n")
		} else {
			for i, issue := range filtered {
				icon := priorityIcons[issue.Priority]
				if icon == "" {
					icon = "  "
				}

				stateLabel := fmt.Sprintf("%-10s", issue.StateGroup)
				issueTitle := issue.Title
				maxTitle := m.w - 20
				if maxTitle < 20 {
					maxTitle = 20
				}
				if len(issueTitle) > maxTitle {
					issueTitle = issueTitle[:maxTitle-3] + "..."
				}

				line := fmt.Sprintf("  %s [%s] %s", icon, stateLabel, issueTitle)
				if i == m.popupCursor {
					line = selectedStyle.Render(line)
				}
				sb.WriteString(line + "\n")
			}
		}
	}

	sb.WriteString("\n" + dimStyle.Render("  ^A/^Z scroll | / filter | s sort | Enter act | r refresh | q/Esc close"))
	return sb.String()
}

func renderIcingaPopup(m tuiModel) string {
	var sb strings.Builder

	title := "Icinga Alerts — Active Problems"
	sortLabel := icingaSortLabels[m.popupSortMode%len(icingaSortLabels)]
	if m.popupSortMode > 0 {
		title += "  [sorted: " + sortLabel + "]"
	}
	sb.WriteString(popupTitleStyle.Render(title))
	sb.WriteString("\n")

	if m.popupFilterActive || m.popupFilter.Value() != "" {
		sb.WriteString("  / " + m.popupFilter.View() + "\n")
	}
	sb.WriteString("\n")

	if m.popupErr != "" {
		sb.WriteString(dimStyle.Render("  Error: "+m.popupErr) + "\n")
	} else if m.icingaProblems == nil {
		sb.WriteString(dimStyle.Render("  Loading...") + "\n")
	} else {
		filtered := filteredIcingaProblems(m)
		if len(filtered) == 0 {
			sb.WriteString(dimStyle.Render("  No active problems. All clear!") + "\n")
		} else {
			for i, p := range filtered {
				stateStyle := stateColors[p.State]
				if stateStyle.GetForeground() == (lipgloss.NoColor{}) {
					stateStyle = dimStyle
				}
				label := icingaStateLabel(p.State)
				header := fmt.Sprintf("  %s  %s / %s", label, p.Host, p.Service)
				if i == m.popupCursor {
					header = selectedStyle.Render(header)
				} else {
					header = stateStyle.Render(header)
				}
				sb.WriteString(header + "\n")

				if p.Output != "" {
					sb.WriteString(dimStyle.Render("     -> "+p.Output) + "\n")
				}
			}
		}
	}

	sb.WriteString("\n" + dimStyle.Render("  ^A/^Z scroll | / filter | s sort | Enter act | r refresh | q/Esc close"))
	return sb.String()
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

	sb.WriteString("\n" + dimStyle.Render("  ^A/^Z select | Enter confirm | Esc cancel"))
	return sb.String()
}
