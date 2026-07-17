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
	case m.picker != nil:
		// In-chat agent/conversation switch reuses the lobby picker as a dialog.
		return m.picker.render(w, 18)
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
func (m *model) renderModal() string {
	a := m.st.pendingApprovals[0]

	// Prefer a JSON-pretty of the tool input; fall back to raw diffs blob.
	var detail string
	if len(a.Input) > 0 {
		input, _ := json.MarshalIndent(a.Input, "", "  ")
		detail = string(input)
	} else if len(a.Diffs) > 0 {
		detail = string(a.Diffs)
	}
	if lines := strings.Split(detail, "\n"); len(lines) > 12 {
		detail = strings.Join(lines[:12], "\n") + "\n…"
	}

	queued := ""
	if n := len(m.st.pendingApprovals) - 1; n > 0 {
		queued = fmt.Sprintf("  (+%d queued)", n)
	}

	options := "[a]llow   [d]eny"
	for i, s := range a.PermissionSuggestions {
		options += fmt.Sprintf("   [%d] %s", i+1, s.Label)
	}
	if a.BlockedPath != "" {
		options = styleError.Render("blocked: "+a.BlockedPath) + "\n" + options
	}

	w := dialogWidth(m.width)
	body := fmt.Sprintf("agent wants to run %s%s\n\n%s\n\n%s",
		styleToolName.Render(a.ToolName), queued, detail, styleInfo.Render(options))
	return dialogBox("tool approval", "a/d or a number", body, w)
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
