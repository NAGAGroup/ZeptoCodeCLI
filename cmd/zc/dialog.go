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
		return m.palette.render(w, 18)
	case m.selection != nil:
		return m.selection.render(w, 18)
	case m.question != nil:
		return dialogBox("agent asks", "esc dismisses = deny",
			m.question.form.View(w-4), w)
	case len(m.st.pendingApprovals) > 0:
		return m.renderModal()
	case m.showHelp:
		return dialogBox("keybindings", "esc or ctrl+g closes",
			m.helpModel.FullHelpView(keys.FullHelp()), w)
	}
	return ""
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
