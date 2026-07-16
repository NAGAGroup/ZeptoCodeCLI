// Theme: one palette keyed by semantic role, resolved for light/dark ONCE
// at startup (lipgloss v2 dropped AdaptiveColor; the background query must
// happen pre-tea anyway — see glamourStyle in main.go).
package main

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
	"github.com/NAGAGroup/ZeptoCodeCLI/internal/ui/form"
)

type themeT struct {
	User   color.Color
	Agent  color.Color
	Tool   color.Color
	OK     color.Color
	Danger color.Color
	Warn   color.Color
	Dim    color.Color
	Accent color.Color
	Text   color.Color
	Blue   color.Color
}

var theme themeT

// initTheme resolves the palette and builds every style. Must run before
// the first render (called from main after background detection).
func initTheme(isDark bool) {
	ld := lipgloss.LightDark(isDark)
	theme = themeT{
		User:   ld(lipgloss.Color("#0087af"), lipgloss.Color("#5fd7ff")), // cyan
		Agent:  ld(lipgloss.Color("#008700"), lipgloss.Color("#87d787")), // green
		Tool:   ld(lipgloss.Color("#af8700"), lipgloss.Color("#d7d787")), // yellow
		OK:     ld(lipgloss.Color("#008700"), lipgloss.Color("#5fd75f")),
		Danger: ld(lipgloss.Color("#d70000"), lipgloss.Color("#ff5f5f")),
		Warn:   ld(lipgloss.Color("#af5f00"), lipgloss.Color("#ffaf5f")),
		Dim:    ld(lipgloss.Color("#8a8a8a"), lipgloss.Color("#6c6c6c")),
		Accent: ld(lipgloss.Color("#5f00af"), lipgloss.Color("#af87ff")), // purple
		Text:   ld(lipgloss.Color("#262626"), lipgloss.Color("#dadada")),
		Blue:   ld(lipgloss.Color("#0087af"), lipgloss.Color("#5fafd7")),
	}

	styleUser = lipgloss.NewStyle().Foreground(theme.User).Bold(true)
	styleAgent = lipgloss.NewStyle().Foreground(theme.Agent).Bold(true)
	styleReasoning = lipgloss.NewStyle().Foreground(theme.Dim).Italic(true)
	styleTool = lipgloss.NewStyle().Foreground(theme.Tool)
	styleToolOK = lipgloss.NewStyle().Foreground(theme.OK)
	styleToolErr = lipgloss.NewStyle().Foreground(theme.Danger)
	styleInfo = lipgloss.NewStyle().Foreground(theme.Dim)
	styleError = lipgloss.NewStyle().Foreground(theme.Danger).Bold(true)
	styleAccent = lipgloss.NewStyle().Foreground(theme.Accent)

	styleStatus = lipgloss.NewStyle().Foreground(theme.Text).Faint(true)
	styleStatusMode = lipgloss.NewStyle().Bold(true)

	styleModal = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Tool).Padding(0, 1)

	styleDiffAdd = lipgloss.NewStyle().Foreground(theme.OK)
	styleDiffRemove = lipgloss.NewStyle().Foreground(theme.Danger)
	styleDiffCtx = lipgloss.NewStyle().Foreground(theme.Dim)

	styleOverlay = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.Accent).Padding(0, 1)
	styleOverlaySel = lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)
	styleOverlayTitle = lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)
	styleOverlayDim = lipgloss.NewStyle().Foreground(theme.Dim)

	styleCompletion = lipgloss.NewStyle().Foreground(theme.Dim)
	styleCompletionSel = lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)

	lipglossStyleKey = lipgloss.NewStyle().Foreground(theme.Accent)

	form.SetAccent(theme.Accent, theme.Dim)
}

// modeColor makes the permission mode legible at a glance: the input border
// wears it. standard=calm blue, acceptEdits=caution yellow, unrestricted=red.
func modeColor(mode protocol.PermissionMode) color.Color {
	switch mode {
	case protocol.ModeStandard:
		return theme.Blue
	case protocol.ModeAcceptEdits:
		return theme.Warn
	case protocol.ModeUnrestricted:
		return theme.Danger
	default:
		return theme.Dim
	}
}

// gutter rails: a colored left bar per entry kind.
func rail(c color.Color) string {
	return lipgloss.NewStyle().Foreground(c).Render("▌ ")
}

var (
	styleUser      lipgloss.Style
	styleAgent     lipgloss.Style
	styleReasoning lipgloss.Style
	styleTool      lipgloss.Style
	styleToolOK    lipgloss.Style
	styleToolErr   lipgloss.Style
	styleInfo      lipgloss.Style
	styleError     lipgloss.Style
	styleAccent    lipgloss.Style

	styleStatus     lipgloss.Style
	styleStatusMode lipgloss.Style

	styleModal lipgloss.Style

	styleDiffAdd    lipgloss.Style
	styleDiffRemove lipgloss.Style
	styleDiffCtx    lipgloss.Style

	styleOverlay      lipgloss.Style
	styleOverlaySel   lipgloss.Style
	styleOverlayTitle lipgloss.Style
	styleOverlayDim   lipgloss.Style

	styleCompletion    lipgloss.Style
	styleCompletionSel lipgloss.Style

	lipglossStyleKey lipgloss.Style
)
