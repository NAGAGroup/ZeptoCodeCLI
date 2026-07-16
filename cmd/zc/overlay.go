// Overlay UI: filterable list overlays (conversation picker, slash-command
// palette, jobs panel) rendered above the input, plus the data sources for
// the ZeptoCode jobs/broker panel (machine-local state, not wire protocol).
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayConversations
	overlayCommands
	overlayJobs
	overlayAgents
	overlayModels
	overlayMgmt // management overlays with a custom onSelect
)

type overlayItem struct {
	id    string // selection payload
	title string
	desc  string
}

type overlay struct {
	kind   overlayKind
	title  string
	items  []overlayItem
	filter string
	sel    int
	// onSelect handles enter for overlayMgmt overlays (custom behavior per
	// management command); nil for the built-in kinds.
	onSelect func(m *model, it overlayItem) tea.Cmd
}

func (o *overlay) filtered() []overlayItem {
	if o.filter == "" {
		return o.items
	}
	type scored struct {
		it    overlayItem
		score int
	}
	var out []scored
	for _, it := range o.items {
		best := -1
		for _, hay := range []string{it.id, it.title, it.desc} {
			if s := fuzzyScore(o.filter, hay); s >= 0 && (best < 0 || s < best) {
				best = s
			}
		}
		if best >= 0 {
			out = append(out, scored{it, best})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].score < out[b].score })
	items := make([]overlayItem, len(out))
	for i, s := range out {
		items[i] = s.it
	}
	return items
}

func (o *overlay) clampSel() {
	n := len(o.filtered())
	if o.sel >= n {
		o.sel = n - 1
	}
	if o.sel < 0 {
		o.sel = 0
	}
}

func (o *overlay) render(width, maxRows int) string {
	items := o.filtered()
	if maxRows < 4 {
		maxRows = 4
	}
	visible := maxRows - 2 // title + filter lines
	start := 0
	if o.sel >= visible {
		start = o.sel - visible + 1
	}
	var b strings.Builder
	b.WriteString(styleOverlayTitle.Render(o.title) +
		styleOverlayDim.Render("  (type to filter, enter selects, esc closes)") + "\n")
	b.WriteString(styleOverlayDim.Render("filter: ") + o.filter + "▏\n")
	for i := start; i < len(items) && i < start+visible; i++ {
		line := items[i].title
		if items[i].desc != "" {
			line += styleOverlayDim.Render("  — " + items[i].desc)
		}
		if i == o.sel {
			line = styleOverlaySel.Render("▸ " + items[i].title)
			if items[i].desc != "" {
				line += styleOverlayDim.Render("  — " + items[i].desc)
			}
		} else {
			line = "  " + line
		}
		b.WriteString(compactOneLine(line, width-6) + "\n")
	}
	if len(items) == 0 {
		b.WriteString(styleOverlayDim.Render("  (no matches)") + "\n")
	}
	w := width - 4
	if w < 20 {
		w = 20
	}
	return styleOverlay.Width(w).Render(strings.TrimRight(b.String(), "\n"))
}

// ── ZeptoCode jobs / broker panel data ──
//
// Jobs are ZeptoCode's persistent background jobs: machine-local manifests
// under ~/.letta/jobs/<id>/manifest.json. The broker (letta-broker) exposes
// /status on :8484 guarded by a bearer token at ~/.letta/tokens/broker.
// Both reads are best-effort: the panel degrades gracefully when absent.

type jobManifest struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Command    string `json:"command"`
	Status     string `json:"status"`
	ExitCode   *int   `json:"exitCode"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
	Remote     any    `json:"remote"`
}

func loadJobs() []jobManifest {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	dirs, err := os.ReadDir(filepath.Join(home, ".letta", "jobs"))
	if err != nil {
		return nil
	}
	var jobs []jobManifest
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(home, ".letta", "jobs", d.Name(), "manifest.json"))
		if err != nil {
			continue
		}
		var j jobManifest
		if json.Unmarshal(raw, &j) == nil && j.ID != "" {
			jobs = append(jobs, j)
		}
	}
	sort.Slice(jobs, func(a, b int) bool { return jobs[a].StartedAt > jobs[b].StartedAt })
	if len(jobs) > 20 {
		jobs = jobs[:20]
	}
	return jobs
}

func brokerStatusLine() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "broker: unknown home"
	}
	token, err := os.ReadFile(filepath.Join(home, ".letta", "tokens", "broker"))
	if err != nil {
		return "broker: no token file"
	}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:8484/status", nil)
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "broker: unreachable"
	}
	defer resp.Body.Close()
	var body map[string]any
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return fmt.Sprintf("broker: http %d", resp.StatusCode)
	}
	parts := []string{"broker: up"}
	for _, k := range []string{"appServer", "app_server", "upstream", "pid", "uptime", "clients"} {
		if v, ok := body[k]; ok {
			parts = append(parts, fmt.Sprintf("%s=%v", k, compactOneLine(fmt.Sprintf("%v", v), 40)))
		}
	}
	return strings.Join(parts, " ")
}

func jobsOverlayItems() []overlayItem {
	items := []overlayItem{{id: "", title: brokerStatusLine(), desc: ""}}
	jobs := loadJobs()
	if len(jobs) == 0 {
		items = append(items, overlayItem{title: "(no jobs found under ~/.letta/jobs)"})
		return items
	}
	for _, j := range jobs {
		glyph := "…"
		switch {
		case j.Status == "running":
			glyph = "▶"
		case j.ExitCode != nil && *j.ExitCode == 0:
			glyph = "✓"
		case j.ExitCode != nil:
			glyph = fmt.Sprintf("✗ exit=%d", *j.ExitCode)
		case j.Status != "":
			glyph = j.Status
		}
		label := j.Label
		if label == "" {
			label = compactOneLine(j.Command, 40)
		}
		items = append(items, overlayItem{
			id:    j.ID,
			title: fmt.Sprintf("%s %s", glyph, label),
			desc:  j.ID + " " + compactOneLine(j.Command, 60),
		})
	}
	return items
}
