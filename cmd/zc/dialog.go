// Floating dialog presentation, after crush's dialog overlay: the active
// modal (question form, mgmt form, picker overlay, tool approval) renders
// as a centered layer composited OVER the full-height transcript with
// lipgloss v2 Canvas/Layer — instead of stealing vertical space between
// transcript and input. Behavior and key routing are unchanged; only
// presentation lives here.
package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"

	"charm.land/lipgloss/v2"
)

// dialogBox wraps body in the shared dialog chrome.
func dialogBox(title, hint, body string, width int) string {
	head := styleOverlayTitle.Render(title)
	if hint != "" {
		head += styleOverlayDim.Render("  " + hint)
	}
	return styleOverlay.Width(width).Render(head + "\n\n" + body)
}

// dialogWidth picks a dialog width for the terminal width.
func dialogWidth(termW int) int {
	w := termW - 8
	if w > 96 {
		w = 96
	}
	if w < 24 {
		w = 24
	}
	return w
}

// activeDialog renders the current modal, or "" when none is active.
// Priority mirrors the key-routing order in handleKey.
func (m *model) activeDialog() string {
	w := dialogWidth(m.width)
	switch {
	case m.palette != nil:
		box := m.palette.render(w, 18)
		// Small dim info line below the palette: agent · model · mode.
		if info := m.paletteInfoBar(); info != "" {
			box += "\n" + info
		}
		return box
	case m.selection != nil:
		return m.selection.render(w, 18)
	case m.question != nil:
		return dialogBox("agent asks", "esc dismisses = deny",
			m.question.form.View(w-4), w)
	case len(m.st.pendingApprovals) > 0:
		return m.renderModal()
	case m.showHelp:
		return m.renderHelp(w)
	}
	return ""
}

// paletteInfoBar renders a dim one-liner shown under the command palette:
// agent name · model · permission mode.
func (m *model) paletteInfoBar() string {
	var parts []string
	if a := m.agentName; a != "" {
		parts = append(parts, a)
	}
	if md := shortModel(m.modelHandle); md != "" {
		parts = append(parts, md)
	}
	mode := string(m.mode)
	if mode == "" {
		mode = "?"
	}
	parts = append(parts, mode)
	return styleInfo.Render("  " + strings.Join(parts, " · "))
}

// helpLines builds the full (unscrolled) help body: a "Commands" section from
// the pushed command catalog (non-hidden) and a "Keyboard shortcuts" section
// from the keymap.
func (m *model) helpLines(w int) []string {
	const keyW = 16
	descW := max(10, w-4-keyW-1)
	var lines []string

	lines = append(lines, styleOverlayTitle.Render("Commands"))
	shown := 0
	for _, c := range m.st.commands {
		if c.Hidden {
			continue
		}
		desc := c.Description
		if c.ArgsHint != "" {
			desc = strings.TrimSpace(desc + " " + c.ArgsHint)
		}
		lines = append(lines, "  "+lipglossStyleKey.Render(padRight(c.ID, keyW))+
			styleInfo.Render(truncPlain(desc, descW)))
		shown++
	}
	if shown == 0 {
		lines = append(lines, styleInfo.Render("  (no commands)"))
	}

	lines = append(lines, "", styleOverlayTitle.Render("Keyboard shortcuts"))
	seen := map[string]bool{}
	for _, col := range keys.FullHelp() {
		for _, b := range col {
			h := b.Help()
			if h.Key == "" || seen[h.Key] {
				continue
			}
			seen[h.Key] = true
			lines = append(lines, "  "+lipglossStyleKey.Render(padRight(h.Key, keyW))+
				styleInfo.Render(truncPlain(h.Desc, descW)))
		}
	}
	return lines
}

// renderHelp renders the scrollable help overlay as a centered modal. The
// visible window is derived from terminal height; ↑/↓ scroll (handled in
// handleKey), esc/ctrl+g/q closes.
func (m *model) renderHelp(w int) string {
	lines := m.helpLines(w)
	total := len(lines)

	maxRows := m.height - 8
	if maxRows < 6 {
		maxRows = 6
	}

	// Clamp scroll to the valid range (guards against height changes).
	maxScroll := max(0, total-maxRows)
	if m.helpScroll < 0 {
		m.helpScroll = 0
	}
	if m.helpScroll > maxScroll {
		m.helpScroll = maxScroll
	}

	end := min(total, m.helpScroll+maxRows)
	view := lines[m.helpScroll:end]

	hint := "esc or ctrl+g closes"
	if total > maxRows {
		hint = fmt.Sprintf("↑/↓ scroll · esc closes (%d–%d/%d)", m.helpScroll+1, end, total)
	}
	return dialogBox("help", hint, strings.Join(view, "\n"), w)
}

// padRight right-pads s with spaces to at least n runes (no truncation).
func padRight(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(r))
}

// truncPlain truncates unstyled text to max runes, appending "…" when cut.
func truncPlain(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max < 1 {
		max = 1
	}
	return string(r[:max-1]) + "…"
}

// renderModal renders the head pending_approvals item as an allow/deny
// dialog. Additional queued approvals are counted in the header.
// diffHunkLine / diffHunk / diffPreview mirror protocol_v2 DiffPreview.
type diffHunkLine struct {
	Type    string `json:"type"` // context|add|remove
	Content string `json:"content"`
}
type diffHunk struct {
	Lines []diffHunkLine `json:"lines"`
}
type diffPreview struct {
	Mode     string     `json:"mode"` // advanced|fallback|unpreviewable
	FileName string     `json:"fileName"`
	Reason   string     `json:"reason,omitempty"`
	Hunks    []diffHunk `json:"hunks,omitempty"`
}

// renderApprovalDiff renders the edit/write diff (colored +/- hunks) for a
// pending approval, or "" if no previewable diff is present.
func (m *model) renderApprovalDiff(a protocol.PendingApproval, w int) string {
	if len(a.Diffs) == 0 {
		return ""
	}
	var previews []diffPreview
	if err := json.Unmarshal(a.Diffs, &previews); err != nil || len(previews) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range previews {
		b.WriteString(styleToolName.Render(p.FileName) + "\n")
		if p.Mode != "advanced" || len(p.Hunks) == 0 {
			if p.Reason != "" {
				b.WriteString(styleInfo.Render("  "+p.Reason) + "\n")
			}
			continue
		}
		shown := 0
		for _, h := range p.Hunks {
			for _, ln := range h.Lines {
				if shown >= 14 {
					b.WriteString(styleInfo.Render("  …") + "\n")
					break
				}
				txt := compactOneLine(ln.Content, max(20, w-2))
				switch ln.Type {
				case "add":
					b.WriteString(styleDiffAdd.Render("+ "+txt) + "\n")
				case "remove":
					b.WriteString(styleDiffRemove.Render("- "+txt) + "\n")
				default:
					b.WriteString(styleInfo.Render("  "+txt) + "\n")
				}
				shown++
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// approvalTitle mirrors the native per-tool question ("Run this command?" etc).
func approvalTitle(tool string) string {
	switch tool {
	case "Bash":
		return "Run this command?"
	case "Edit", "MultiEdit":
		return "Make this edit?"
	case "Write":
		return "Write this file?"
	case "Read":
		return "Read this file?"
	default:
		return "Approve " + tool + "?"
	}
}

// approvalDetail renders the tool-specific body ($ command / file diff / params).
func (m *model) approvalDetail(a protocol.PendingApproval, w int) string {
	switch a.ToolName {
	case "Bash":
		if cmd, ok := a.Input["command"].(string); ok {
			return styleToolParam.Render("$ " + compactOneLine(cmd, max(20, w-4)))
		}
	case "Edit", "MultiEdit", "Write":
		// Prefer a rendered diff when present (task: diffviews).
		if d := m.renderApprovalDiff(a, w); d != "" {
			return d
		}
	}
	if len(a.Input) > 0 {
		b, _ := json.MarshalIndent(a.Input, "", "  ")
		detail := string(b)
		if lines := strings.Split(detail, "\n"); len(lines) > 12 {
			detail = strings.Join(lines[:12], "\n") + "\n…"
		}
		return styleToolParam.Render(detail)
	}
	return ""
}

func (m *model) renderModal() string {
	a := m.st.pendingApprovals[0]
	w := dialogWidth(m.width)

	var b strings.Builder
	b.WriteString(styleToolName.Render(approvalTitle(a.ToolName)))
	if n := len(m.st.pendingApprovals) - 1; n > 0 {
		b.WriteString(styleInfo.Render(fmt.Sprintf("  (+%d queued)", n)))
	}
	b.WriteString("\n\n")
	if detail := m.approvalDetail(a, w-4); detail != "" {
		b.WriteString(detail)
		b.WriteString("\n\n")
	}
	if a.BlockedPath != "" {
		b.WriteString(styleError.Render("blocked: "+a.BlockedPath) + "\n\n")
	}

	// Numbered options: Yes · Yes+<suggestion> · No.
	labels := []string{"Yes"}
	for _, s := range a.PermissionSuggestions {
		labels = append(labels, s.Label)
	}
	labels = append(labels, "No, and tell Letta Code what to do differently")
	for i, label := range labels {
		cursor := "  "
		style := styleInfo
		if i == m.approvalSel {
			cursor = styleAccent.Render("❯ ")
			style = styleAccent
		}
		b.WriteString(fmt.Sprintf("%s%s\n", cursor, style.Render(fmt.Sprintf("%d. %s", i+1, label))))
	}
	return dialogBox("", "↑/↓ or number · enter select · esc cancel", b.String(), w)
}

// compositeDialog centers box over base. Layer x/y/z are resolved by the
// Compositor (Canvas.Compose alone draws at the origin — the positioning
// pass lives in lipgloss's Compositor).
func compositeDialog(base, box string, termW, termH int) string {
	bw, bh := lipgloss.Width(box), lipgloss.Height(box)
	return compositeAt(base, box, (termW-bw)/2, (termH-bh)/2)
}

// compositeAt overlays box on base at a fixed position (floating popups
// anchored to the input, e.g. completions).
func compositeAt(base, box string, x, y int) string {
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	comp := lipgloss.NewCompositor(
		lipgloss.NewLayer(base).Z(0),
		lipgloss.NewLayer(box).X(x).Y(y).Z(1),
	)
	return comp.Render()
}
