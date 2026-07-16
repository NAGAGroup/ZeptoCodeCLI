// Theme: one palette keyed by semantic role, resolved for light/dark ONCE
// at startup (lipgloss v2 dropped AdaptiveColor; the background query must
// happen pre-tea anyway — see glamourStyle in main.go).
package main

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/charmtone"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
	"github.com/NAGAGroup/ZeptoCodeCLI/internal/ui/form"
)

// Icons (after crush's icon set).
const (
	iconToolPending = "●"
	iconToolOK      = "✓"
	iconToolErr     = "×"
	iconToolWait    = "◍"
	iconAgent       = "●"
	iconUser        = "▌"
	iconQueue       = "⧗"
	iconSubagent    = "⚙"
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
	// The charmtone palette (crush's), dark-first with legible light picks.
	ld := lipgloss.LightDark(isDark)
	theme = themeT{
		User:   ld(charmtone.Ox, charmtone.Malibu),      // blue
		Agent:  ld(charmtone.Guac, charmtone.Julep),     // green
		Tool:   ld(charmtone.Mustard, charmtone.Zest),   // yellow
		OK:     ld(charmtone.Guac, charmtone.Julep),
		Danger: ld(charmtone.Sriracha, charmtone.Cherry),
		Warn:   ld(charmtone.Cumin, charmtone.Citron),
		Dim:    ld(charmtone.Squid, charmtone.Smoke),
		Accent: ld(charmtone.Violet, charmtone.Charple), // purple
		Text:   ld(charmtone.Char, charmtone.Salt),
		Blue:   ld(charmtone.Sapphire, charmtone.Malibu),
	}

	styleUser = lipgloss.NewStyle().Foreground(theme.User).Bold(true)
	styleAgent = lipgloss.NewStyle().Foreground(theme.Agent).Bold(true)
	styleReasoning = lipgloss.NewStyle().Foreground(theme.Dim).Italic(true)
	styleTool = lipgloss.NewStyle().Foreground(theme.Tool)
	styleToolOK = lipgloss.NewStyle().Foreground(theme.OK)
	styleToolErr = lipgloss.NewStyle().Foreground(theme.Danger)
	styleToolName = lipgloss.NewStyle().Foreground(theme.Text).Bold(true)
	styleToolParam = lipgloss.NewStyle().Foreground(theme.Dim)
	styleToolBody = lipgloss.NewStyle().Foreground(theme.Dim)
	styleInfo = lipgloss.NewStyle().Foreground(theme.Dim)
	styleError = lipgloss.NewStyle().Foreground(theme.Danger).Bold(true)
	styleAccent = lipgloss.NewStyle().Foreground(theme.Accent)

	styleStatus = lipgloss.NewStyle().Foreground(theme.Text).Faint(true)
	styleStatusMode = lipgloss.NewStyle().Bold(true)

	styleModal = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Tool).Padding(0, 1)

	// Diff lines carry a subtle background tint (charmtone's diff ramps),
	// like crush's diffview.
	styleDiffAdd = lipgloss.NewStyle().Foreground(ld(charmtone.Guac, charmtone.Julep)).
		Background(ld(lipgloss.Color("#e8f5e2"), charmtone.Spinach))
	styleDiffRemove = lipgloss.NewStyle().Foreground(ld(charmtone.Sriracha, charmtone.Cherry)).
		Background(ld(lipgloss.Color("#fbeaea"), charmtone.Toast))
	styleDiffCtx = lipgloss.NewStyle().Foreground(theme.Dim)

	styleModePill = lipgloss.NewStyle().Bold(true).Padding(0, 1).
		Foreground(ld(charmtone.Salt, charmtone.Pepper))

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
	styleToolName  lipgloss.Style
	styleToolParam lipgloss.Style
	styleToolBody  lipgloss.Style
	styleInfo      lipgloss.Style
	styleError     lipgloss.Style
	styleAccent    lipgloss.Style

	styleStatus     lipgloss.Style
	styleStatusMode lipgloss.Style

	styleModal lipgloss.Style

	styleDiffAdd    lipgloss.Style
	styleDiffRemove lipgloss.Style
	styleDiffCtx    lipgloss.Style

	styleModePill lipgloss.Style

	styleOverlay      lipgloss.Style
	styleOverlaySel   lipgloss.Style
	styleOverlayTitle lipgloss.Style
	styleOverlayDim   lipgloss.Style

	styleCompletion    lipgloss.Style
	styleCompletionSel lipgloss.Style

	lipglossStyleKey lipgloss.Style
)
