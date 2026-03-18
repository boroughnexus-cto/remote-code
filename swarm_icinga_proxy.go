package main

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// IcingaService is the simplified service data returned to TUI.
type IcingaService struct {
	Host       string `json:"host"`
	Service    string `json:"service"`
	State      int    `json:"state"`       // 0=OK 1=WARN 2=CRIT 3=UNKNOWN
	StateType  int    `json:"state_type"`  // 0=SOFT 1=HARD
	Output     string `json:"output"`
	LastChange int64  `json:"last_change"` // unix seconds
	Acked      bool   `json:"acked"`
	Downtime   bool   `json:"downtime"`
}

func icingaBaseURL() string {
	if u := os.Getenv("ICINGA_URL"); u != "" {
		return u
	}
	return "https://10.0.0.2:5665"
}

func icingaCreds() (string, string) {
	user := os.Getenv("ICINGA_USER")
	pass := os.Getenv("ICINGA_PASS")
	if user == "" {
		user = "root"
	}
	if pass == "" {
		pass = "MHdszdvH7tnE2kO-iL2WYKv-t52AkiwL"
	}
	return user, pass
}

func newIcingaHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
		Timeout: 15 * time.Second,
	}
}

// handleIcingaAPI handles GET /api/icinga/services
func handleIcingaAPI(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 0 || parts[0] != "services" {
		http.Error(w, `{"error":"only /api/icinga/services supported"}`, http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	hc := newIcingaHTTPClient()
	base := icingaBaseURL()
	user, pass := icingaCreds()

	req, err := http.NewRequestWithContext(r.Context(), "GET", base+"/v1/objects/services", nil)
	if err != nil {
		http.Error(w, `{"error":"bad request"}`, http.StatusInternalServerError)
		return
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Accept", "application/json")

	// Filter to only the attrs we need.
	q := req.URL.Query()
	for _, attr := range []string{
		"display_name", "host_name", "state", "state_type",
		"last_check_result", "last_state_change",
		"acknowledgement", "downtime_depth",
	} {
		q.Add("attrs", attr)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := hc.Do(req)
	if err != nil {
		http.Error(w, `{"error":"icinga unreachable: `+err.Error()+`"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var raw struct {
		Results []struct {
			Name  string `json:"name"`
			Attrs struct {
				DisplayName     string  `json:"display_name"`
				HostName        string  `json:"host_name"`
				State           float64 `json:"state"`
				StateType       float64 `json:"state_type"`
				LastStateChange float64 `json:"last_state_change"`
				Acknowledgement float64 `json:"acknowledgement"`
				DowntimeDepth   float64 `json:"downtime_depth"`
				LastCheckResult *struct {
					Output string `json:"output"`
				} `json:"last_check_result"`
			} `json:"attrs"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		http.Error(w, `{"error":"parse error"}`, http.StatusBadGateway)
		return
	}

	services := make([]IcingaService, 0, len(raw.Results))
	for _, r := range raw.Results {
		output := ""
		if r.Attrs.LastCheckResult != nil {
			output = r.Attrs.LastCheckResult.Output
			// Trim to first line for the summary view.
			if idx := strings.IndexByte(output, '\n'); idx >= 0 {
				output = output[:idx]
			}
		}
		name := r.Attrs.DisplayName
		if name == "" {
			// Icinga names services as "host!service"
			if _, svc, ok := strings.Cut(r.Name, "!"); ok {
				name = svc
			} else {
				name = r.Name
			}
		}
		services = append(services, IcingaService{
			Host:       r.Attrs.HostName,
			Service:    name,
			State:      int(r.Attrs.State),
			StateType:  int(r.Attrs.StateType),
			Output:     output,
			LastChange: int64(r.Attrs.LastStateChange),
			Acked:      r.Attrs.Acknowledgement > 0,
			Downtime:   r.Attrs.DowntimeDepth > 0,
		})
	}

	// Sort: CRIT→WARN→UNKNOWN→OK; within group, most recently changed first.
	stateOrder := map[int]int{2: 0, 1: 1, 3: 2, 0: 3}
	sort.SliceStable(services, func(i, j int) bool {
		oi := stateOrder[services[i].State]
		oj := stateOrder[services[j].State]
		if oi != oj {
			return oi < oj
		}
		return services[i].LastChange > services[j].LastChange
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(services) //nolint:errcheck
}
