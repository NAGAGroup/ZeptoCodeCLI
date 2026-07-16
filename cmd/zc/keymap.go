// Central keymap: every binding declared once, surfaced via bubbles/help.
package main

import (
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
)

type keymapT struct {
	Send          key.Binding
	Newline       key.Binding
	ClearOrAbort  key.Binding
	Quit          key.Binding
	Palette       key.Binding
	Conversations key.Binding
	Agents        key.Binding
	Jobs          key.Binding
	Reasoning     key.Binding
	ToolOutput    key.Binding
	Mode          key.Binding
	HistoryUp     key.Binding
	HistoryDown   key.Binding
	ScrollUp      key.Binding
	ScrollDown    key.Binding
	Complete      key.Binding
	Help          key.Binding
}

var keys = keymapT{
	Send:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
	Newline:       key.NewBinding(key.WithKeys("alt+enter"), key.WithHelp("alt+enter", "newline")),
	ClearOrAbort:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear input / abort turn")),
	Quit:          key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit (2× mid-turn)")),
	Palette:       key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "command palette")),
	Conversations: key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "conversations")),
	Agents:        key.NewBinding(key.WithKeys("ctrl+a"), key.WithHelp("ctrl+a", "switch agent")),
	Jobs:          key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("ctrl+j", "jobs/broker panel")),
	Reasoning:     key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("ctrl+r", "toggle reasoning")),
	ToolOutput:    key.NewBinding(key.WithKeys("ctrl+o"), key.WithHelp("ctrl+o", "toggle tool output")),
	Mode:          key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "cycle permission mode")),
	HistoryUp:     key.NewBinding(key.WithKeys("up"), key.WithHelp("↑/↓", "input history")),
	HistoryDown:   key.NewBinding(key.WithKeys("down")),
	ScrollUp:      key.NewBinding(key.WithKeys("pgup", "ctrl+u"), key.WithHelp("pgup/pgdn·wheel", "scroll")),
	ScrollDown:    key.NewBinding(key.WithKeys("pgdown", "ctrl+d")),
	Complete:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "complete / and @")),
	Help:          key.NewBinding(key.WithKeys("ctrl+g"), key.WithHelp("ctrl+g", "help")),
}

// ShortHelp implements help.KeyMap (statusline hints).
func (k keymapT) ShortHelp() []key.Binding {
	return []key.Binding{k.Palette, k.Conversations, k.Mode, k.Help}
}

// FullHelp implements help.KeyMap (ctrl+g view).
func (k keymapT) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Send, k.Newline, k.HistoryUp, k.Complete, k.ClearOrAbort, k.Quit},
		{k.Palette, k.Conversations, k.Agents, k.Jobs, k.ScrollUp},
		{k.Mode, k.Reasoning, k.ToolOutput, k.Help},
	}
}

func newHelp() help.Model {
	h := help.New()
	h.Styles.ShortKey = lipglossStyleKey
	h.Styles.ShortDesc = styleInfo
	h.Styles.FullKey = lipglossStyleKey
	h.Styles.FullDesc = styleInfo
	return h
}
