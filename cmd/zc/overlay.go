// Overlay UI: a filterable list overlay used for the lobby pickers (agent
// picker, conversation picker). The server provides the options as state
// (SPEC rule 1); the client owns all selection presentation. Typing filters,
// ↑/↓ moves, enter commits the choice as a select_* event.
package main

import (
	"sort"
	"strings"
)

type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayAgents
	overlayConversations
)

type overlayItem struct {
	id    string // selection payload (agent_id / conversation_id / "new")
	title string
	desc  string
}

type overlay struct {
	kind   overlayKind
	title  string
	items  []overlayItem
	filter string
	sel    int
}

// fuzzyScore is a subsequence match; lower is better, -1 = no match.
func fuzzyScore(needle, hay string) int {
	needle, hay = strings.ToLower(needle), strings.ToLower(hay)
	if needle == "" {
		return 0
	}
	score, j := 0, 0
	for i := 0; i < len(hay) && j < len(needle); i++ {
		if hay[i] == needle[j] {
			j++
		} else {
			score++
		}
	}
	if j < len(needle) {
		return -1
	}
	return score
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
		styleOverlayDim.Render("  (type to filter, enter selects, esc quits)") + "\n")
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
