// Theme: one adaptive palette keyed by semantic role. lipgloss degrades
// truecolor → 256 → 16 automatically, so these stay honest on bare terminals.
package main

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
)

type themeT struct {
	User   lipgloss.AdaptiveColor
	Agent  lipgloss.AdaptiveColor
	Tool   lipgloss.AdaptiveColor
	OK     lipgloss.AdaptiveColor
	Danger lipgloss.AdaptiveColor
	Warn   lipgloss.AdaptiveColor
	Dim    lipgloss.AdaptiveColor
	Accent lipgloss.AdaptiveColor
	Text   lipgloss.AdaptiveColor
}

var theme = themeT{
	User:   lipgloss.AdaptiveColor{Light: "#0087af", Dark: "#5fd7ff"}, // cyan
	Agent:  lipgloss.AdaptiveColor{Light: "#008700", Dark: "#87d787"}, // green
	Tool:   lipgloss.AdaptiveColor{Light: "#af8700", Dark: "#d7d787"}, // yellow
	OK:     lipgloss.AdaptiveColor{Light: "#008700", Dark: "#5fd75f"},
	Danger: lipgloss.AdaptiveColor{Light: "#d70000", Dark: "#ff5f5f"},
	Warn:   lipgloss.AdaptiveColor{Light: "#af5f00", Dark: "#ffaf5f"},
	Dim:    lipgloss.AdaptiveColor{Light: "#8a8a8a", Dark: "#6c6c6c"},
	Accent: lipgloss.AdaptiveColor{Light: "#5f00af", Dark: "#af87ff"}, // purple
	Text:   lipgloss.AdaptiveColor{Light: "#262626", Dark: "#dadada"},
}

// modeColor makes the permission mode legible at a glance: the input border
// wears it. standard=calm blue, acceptEdits=caution yellow, unrestricted=red.
func modeColor(mode protocol.PermissionMode) lipgloss.AdaptiveColor {
	switch mode {
	case protocol.ModeStandard:
		return lipgloss.AdaptiveColor{Light: "#0087af", Dark: "#5fafd7"}
	case protocol.ModeAcceptEdits:
		return theme.Warn
	case protocol.ModeUnrestricted:
		return theme.Danger
	default:
		return theme.Dim
	}
}

// gutter rails: a colored left bar per entry kind.
func rail(c lipgloss.AdaptiveColor) string {
	return lipgloss.NewStyle().Foreground(c).Render("▌ ")
}

var (
	styleUser      = lipgloss.NewStyle().Foreground(theme.User).Bold(true)
	styleAgent     = lipgloss.NewStyle().Foreground(theme.Agent).Bold(true)
	styleReasoning = lipgloss.NewStyle().Foreground(theme.Dim).Italic(true)
	styleTool      = lipgloss.NewStyle().Foreground(theme.Tool)
	styleToolOK    = lipgloss.NewStyle().Foreground(theme.OK)
	styleToolErr   = lipgloss.NewStyle().Foreground(theme.Danger)
	styleInfo      = lipgloss.NewStyle().Foreground(theme.Dim)
	styleError     = lipgloss.NewStyle().Foreground(theme.Danger).Bold(true)
	styleAccent    = lipgloss.NewStyle().Foreground(theme.Accent)

	styleStatus     = lipgloss.NewStyle().Foreground(theme.Text).Faint(true)
	styleStatusMode = lipgloss.NewStyle().Bold(true)

	styleModal = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(theme.Tool).Padding(0, 1)

	styleDiffAdd    = lipgloss.NewStyle().Foreground(theme.OK)
	styleDiffRemove = lipgloss.NewStyle().Foreground(theme.Danger)
	styleDiffCtx    = lipgloss.NewStyle().Foreground(theme.Dim)

	styleOverlay      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.Accent).Padding(0, 1)
	styleOverlaySel   = lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)
	styleOverlayTitle = lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)
	styleOverlayDim   = lipgloss.NewStyle().Foreground(theme.Dim)

	styleCompletion    = lipgloss.NewStyle().Foreground(theme.Dim)
	styleCompletionSel = lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)

	lipglossStyleKey = lipgloss.NewStyle().Foreground(theme.Accent)
)
