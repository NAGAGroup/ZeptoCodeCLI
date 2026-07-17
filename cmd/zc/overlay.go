// Overlay UI: a filterable list overlay used for the lobby pickers (agent
// picker, conversation picker), the slash-command palette, and the generic
// tagged selection picker (SPEC R23). The server provides the options as
// state (SPEC rule 1); the client owns all selection presentation. Typing
// filters, ↑/↓ moves, enter commits the choice as a select_* / execute_command
// / selection_choice event.
package main

import (
	"sort"
	"strings"
)

type overlayKind int

const (
	overlayNone overlayKind = iota
	overlayPalette   // slash-command palette over the command_catalog
	overlaySelection // unified tagged selection (agent/conversation/model pickers, multi, questions)
)

type overlayItem struct {
	id       string // selection payload (agent_id / command id / selection item id)
	title    string
	desc     string
	badge    string // e.g. "local", "no key", "★", command source
	header   bool   // non-selectable group-label row
	disabled bool   // rendered dim, not pickable
}

type overlay struct {
	kind   overlayKind
	title  string
	items  []overlayItem
	filter string
	sel    int

	// tagged-selection extras (kind == overlaySelection)
	tag      string          // echoed to the server in selection_choice
	multi    bool            // space toggles, enter confirms
	selected map[string]bool // multi-select toggle set (item id -> chosen)
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

// matchScore is the best fuzzy score of an item across its searchable fields.
func matchScore(filter string, it overlayItem) int {
	best := -1
	for _, hay := range []string{it.id, it.title, it.desc} {
		if s := fuzzyScore(filter, hay); s >= 0 && (best < 0 || s < best) {
			best = s
		}
	}
	return best
}

func (o *overlay) hasHeaders() bool {
	for i := range o.items {
		if o.items[i].header {
			return true
		}
	}
	return false
}

// filtered returns the visible rows for the current filter. Grouped overlays
// (those with header rows) preserve group order and drop empty groups; flat
// overlays (lobby pickers, palette) rank by fuzzy score.
func (o *overlay) filtered() []overlayItem {
	if o.filter == "" {
		return o.items
	}
	if o.hasHeaders() {
		return o.filterGrouped()
	}
	type scored struct {
		it    overlayItem
		score int
	}
	var out []scored
	for _, it := range o.items {
		if s := matchScore(o.filter, it); s >= 0 {
			out = append(out, scored{it, s})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].score < out[b].score })
	items := make([]overlayItem, len(out))
	for i, s := range out {
		items[i] = s.it
	}
	return items
}

// filterGrouped keeps items in declaration order and only emits a group
// header when at least one item under it survives the filter.
func (o *overlay) filterGrouped() []overlayItem {
	var out []overlayItem
	var pending *overlayItem
	emitted := false
	for i := range o.items {
		it := o.items[i]
		if it.header {
			pending = &o.items[i]
			emitted = false
			continue
		}
		if matchScore(o.filter, it) >= 0 {
			if pending != nil && !emitted {
				out = append(out, *pending)
				emitted = true
			}
			out = append(out, it)
		}
	}
	return out
}

// selectableAt reports whether the row at i can be highlighted/picked.
func selectableAt(items []overlayItem, i int) bool {
	return i >= 0 && i < len(items) && !items[i].header && !items[i].disabled
}

// move steps the selection by delta, skipping headers and disabled rows.
func (o *overlay) move(delta int) {
	items := o.filtered()
	n := len(items)
	if n == 0 {
		o.sel = 0
		return
	}
	i := o.sel + delta
	for i >= 0 && i < n && !selectableAt(items, i) {
		i += delta
	}
	if i < 0 || i >= n {
		return // no selectable row that direction; keep current
	}
	o.sel = i
}

// clampSel keeps sel in range and landed on a selectable row.
func (o *overlay) clampSel() {
	items := o.filtered()
	n := len(items)
	if n == 0 {
		o.sel = 0
		return
	}
	if o.sel < 0 {
		o.sel = 0
	}
	if o.sel >= n {
		o.sel = n - 1
	}
	if selectableAt(items, o.sel) {
		return
	}
	for i := o.sel; i < n; i++ {
		if selectableAt(items, i) {
			o.sel = i
			return
		}
	}
	for i := o.sel; i >= 0; i-- {
		if selectableAt(items, i) {
			o.sel = i
			return
		}
	}
}

// current returns the highlighted item, or false when none is selectable.
func (o *overlay) current() (overlayItem, bool) {
	items := o.filtered()
	if o.sel < 0 || o.sel >= len(items) || !selectableAt(items, o.sel) {
		return overlayItem{}, false
	}
	return items[o.sel], true
}

func (o *overlay) toggle(id string) {
	if o.selected == nil {
		o.selected = map[string]bool{}
	}
	if o.selected[id] {
		delete(o.selected, id)
	} else {
		o.selected[id] = true
	}
}

// chosen returns the multi-select ids in declaration order.
func (o *overlay) chosen() []string {
	var out []string
	for i := range o.items {
		if o.items[i].header {
			continue
		}
		if o.selected[o.items[i].id] {
			out = append(out, o.items[i].id)
		}
	}
	return out
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

	hint := "  (type to filter, enter selects, esc quits)"
	if o.multi {
		hint = "  (space toggles, enter confirms, esc cancels)"
	}
	var b strings.Builder
	b.WriteString(styleOverlayTitle.Render(o.title) + styleOverlayDim.Render(hint) + "\n")
	b.WriteString(styleOverlayDim.Render("filter: ") + o.filter + "▏\n")

	for i := start; i < len(items) && i < start+visible; i++ {
		it := items[i]
		if it.header {
			b.WriteString(styleOverlayDim.Render("── "+it.title+" ──") + "\n")
			continue
		}

		mark := "  "
		if o.multi {
			if o.selected[it.id] {
				mark = "[x] "
			} else {
				mark = "[ ] "
			}
		}

		label := it.title
		if it.badge != "" {
			label += " " + styleOverlayDim.Render("("+it.badge+")")
		}
		if it.disabled {
			label = styleOverlayDim.Render(it.title)
		}

		var line string
		if i == o.sel && !it.disabled {
			line = styleOverlaySel.Render("▸ " + mark + it.title)
			if it.badge != "" {
				line += " " + styleOverlayDim.Render("("+it.badge+")")
			}
		} else {
			line = "  " + mark + label
		}
		if it.desc != "" {
			line += styleOverlayDim.Render("  — " + it.desc)
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
