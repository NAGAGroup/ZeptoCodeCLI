// Floating dialog presentation, after crush's dialog overlay: the active
// modal (question form, mgmt form, picker overlay, tool approval) renders
// as a centered layer composited OVER the full-height transcript with
// lipgloss v2 Canvas/Layer — instead of stealing vertical space between
// transcript and input. Behavior and key routing are unchanged; only
// presentation lives here.
package main

import (
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
	case m.question != nil:
		return dialogBox("agent asks", "esc dismisses = deny",
			m.question.form.View(w-4), w)
	case m.mgmt != nil:
		return dialogBox(m.mgmt.title, "esc cancels",
			m.mgmt.form.View(w-4), w)
	case m.pager != nil:
		return m.pager.render(m.width, m.height)
	case m.overlay != nil:
		return m.overlay.render(w, 18)
	case len(m.approvals) > 0:
		return m.renderModal()
	case m.showHelp:
		return dialogBox("keybindings", "esc or ctrl+g closes",
			m.helpModel.FullHelpView(keys.FullHelp()), w)
	}
	return ""
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
