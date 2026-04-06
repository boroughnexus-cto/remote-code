package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func renderPlanePopup(m tuiModel) string {
	var sb strings.Builder
	sb.WriteString(popupTitleStyle.Render("Plane Issues — Backlog & In Progress"))
	sb.WriteString("\n\n")

	if m.popupErr != "" {
		sb.WriteString(dimStyle.Render("  Error: "+m.popupErr) + "\n")
	} else if m.planeIssues == nil {
		sb.WriteString(dimStyle.Render("  Loading...") + "\n")
	} else if len(m.planeIssues) == 0 {
		sb.WriteString(dimStyle.Render("  No issues found.") + "\n")
	} else {
		for i, issue := range m.planeIssues {
			icon := priorityIcons[issue.Priority]
			if icon == "" {
				icon = "  "
			}

			stateLabel := fmt.Sprintf("%-10s", issue.StateGroup)
			title := issue.Title
			maxTitle := m.w - 20
			if maxTitle < 20 {
				maxTitle = 20
			}
			if len(title) > maxTitle {
				title = title[:maxTitle-3] + "..."
			}

			line := fmt.Sprintf("  %s [%s] %s", icon, stateLabel, title)
			if i == m.popupCursor {
				line = selectedStyle.Render(line)
			}
			sb.WriteString(line + "\n")
		}
	}

	sb.WriteString("\n" + dimStyle.Render("  ^A/^Z scroll | r refresh | q/Esc close"))
	return sb.String()
}

func renderIcingaPopup(m tuiModel) string {
	var sb strings.Builder
	sb.WriteString(popupTitleStyle.Render("Icinga Alerts — Active Problems"))
	sb.WriteString("\n\n")

	if m.popupErr != "" {
		sb.WriteString(dimStyle.Render("  Error: "+m.popupErr) + "\n")
	} else if m.icingaProblems == nil {
		sb.WriteString(dimStyle.Render("  Loading...") + "\n")
	} else if len(m.icingaProblems) == 0 {
		sb.WriteString(dimStyle.Render("  No active problems. All clear!") + "\n")
	} else {
		for i, p := range m.icingaProblems {
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

	sb.WriteString("\n" + dimStyle.Render("  ^A/^Z scroll | r refresh | q/Esc close"))
	return sb.String()
}
