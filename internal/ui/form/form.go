// Package form is zc's native replacement for huh on the bubbletea v2
// stack: a sequence of field "pages" (select, multi-select, input, text,
// confirm) advanced with enter, with per-field hide functions for
// conditional flow. Modeled on the way crush builds its question dialogs —
// imperative structs driven by the host model, not nested tea models.
//
// Keys: ↑/↓ move within a field · space toggles multi-select · enter
// confirms the field and advances (submits on the last visible field) ·
// shift+tab goes back. Esc/abort is the HOST's job — the form never
// swallows it.
package form

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// State is the form lifecycle state.
type State int

const (
	StateEditing State = iota
	StateCompleted
)

// Styles are set once by the host application at startup.
type Styles struct {
	Title       lipgloss.Style
	Description lipgloss.Style
	Option      lipgloss.Style
	Selected    lipgloss.Style
	Help        lipgloss.Style
	Rail        lipgloss.Style // left gutter bar on the focused field
}

var styles = Styles{
	Title:       lipgloss.NewStyle().Bold(true),
	Description: lipgloss.NewStyle().Faint(true),
	Option:      lipgloss.NewStyle(),
	Selected:    lipgloss.NewStyle().Bold(true),
	Help:        lipgloss.NewStyle().Faint(true),
	Rail:        lipgloss.NewStyle(),
}

// SetStyles installs the host theme.
func SetStyles(s Styles) { styles = s }

// SetAccent is a convenience for hosts that only want to tint the chrome.
func SetAccent(c color.Color, dim color.Color) {
	styles.Title = styles.Title.Foreground(c)
	styles.Selected = styles.Selected.Foreground(c)
	styles.Rail = styles.Rail.Foreground(c)
	styles.Description = styles.Description.Foreground(dim)
	styles.Help = styles.Help.Foreground(dim)
}

// Field is one page of the form.
type Field interface {
	// Update handles input while the field is focused. done=true means the
	// field was confirmed (advance to the next one).
	Update(msg tea.Msg) (cmd tea.Cmd, done bool)
	// View renders the field. help is the footer hint line to embed.
	View(width int) string
	// Focus/Blur manage inner bubbles state.
	Focus() tea.Cmd
	Blur()
	// Hidden fields are skipped during navigation.
	Hidden() bool
}

// Form is a sequence of fields shown one at a time.
type Form struct {
	fields []Field
	idx    int
	state  State
}

// New creates a form. Call Init through the tea loop before first render.
func New(fields ...Field) *Form {
	return &Form{fields: fields, idx: -1}
}

// State reports the lifecycle state.
func (f *Form) State() State { return f.state }

// Init focuses the first visible field.
func (f *Form) Init() tea.Cmd {
	return f.advance(1)
}

// advance moves focus by dir (skipping hidden fields); completing past the
// end marks the form done. Returns the new field's focus cmd.
func (f *Form) advance(dir int) tea.Cmd {
	if f.idx >= 0 && f.idx < len(f.fields) {
		f.fields[f.idx].Blur()
	}
	i := f.idx + dir
	for i >= 0 && i < len(f.fields) && f.fields[i].Hidden() {
		i += dir
	}
	if i >= len(f.fields) {
		f.state = StateCompleted
		return nil
	}
	if i < 0 {
		i = f.idx // can't go back past the first visible field
		if i < 0 {
			return nil
		}
	}
	f.idx = i
	return f.fields[f.idx].Focus()
}

// Update routes input to the focused field and handles page navigation.
func (f *Form) Update(msg tea.Msg) tea.Cmd {
	if f.state == StateCompleted || f.idx < 0 || f.idx >= len(f.fields) {
		return nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "shift+tab" {
		return f.advance(-1)
	}
	cmd, done := f.fields[f.idx].Update(msg)
	if done {
		return tea.Batch(cmd, f.advance(1))
	}
	return cmd
}

// View renders the focused field.
func (f *Form) View(width int) string {
	if f.idx < 0 || f.idx >= len(f.fields) {
		return ""
	}
	return f.fields[f.idx].View(width)
}

// ── shared field base ──

type base struct {
	title    string
	desc     string
	hideFunc func() bool
}

func (b *base) Hidden() bool { return b.hideFunc != nil && b.hideFunc() }

func (b *base) header() string {
	out := styles.Title.Render(b.title)
	if b.desc != "" {
		out += "\n" + styles.Description.Render(b.desc)
	}
	return out
}

func railed(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = styles.Rail.Render("┃ ") + lines[i]
	}
	return strings.Join(lines, "\n")
}

// ── Select ──

// Option pairs a display label with a value.
type Option struct {
	Label string
	Value string
}

// Select is a single-choice list.
type Select struct {
	base
	options []Option
	cursor  int
	value   *string
}

func NewSelect(title string) *Select { return &Select{base: base{title: title}} }

func (s *Select) Description(d string) *Select        { s.desc = d; return s }
func (s *Select) Options(opts ...Option) *Select      { s.options = opts; return s }
func (s *Select) Value(v *string) *Select             { s.value = v; return s }
func (s *Select) WithHide(fn func() bool) *Select     { s.hideFunc = fn; return s }

func (s *Select) Focus() tea.Cmd {
	// Restore cursor from the bound value when revisiting.
	if s.value != nil && *s.value != "" {
		for i, o := range s.options {
			if o.Value == *s.value {
				s.cursor = i
				break
			}
		}
	}
	s.sync()
	return nil
}
func (s *Select) Blur() {}

func (s *Select) sync() {
	if s.value != nil && s.cursor < len(s.options) {
		*s.value = s.options[s.cursor].Value
	}
}

func (s *Select) Update(msg tea.Msg) (tea.Cmd, bool) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, false
	}
	switch key.String() {
	case "up", "k", "ctrl+p":
		if s.cursor > 0 {
			s.cursor--
		}
		s.sync()
	case "down", "j", "ctrl+n":
		if s.cursor < len(s.options)-1 {
			s.cursor++
		}
		s.sync()
	case "enter":
		s.sync()
		return nil, true
	}
	return nil, false
}

func (s *Select) View(width int) string {
	var b strings.Builder
	b.WriteString(s.header() + "\n")
	for i, o := range s.options {
		label := o.Label
		if i == s.cursor {
			b.WriteString(styles.Selected.Render("▸ "+label) + "\n")
		} else {
			b.WriteString(styles.Option.Render("  "+label) + "\n")
		}
	}
	b.WriteString(styles.Help.Render("↑/↓ choose · enter select"))
	return railed(strings.TrimRight(b.String(), "\n"))
}

// ── MultiSelect ──

// MultiSelect is a multiple-choice list toggled with space.
type MultiSelect struct {
	base
	options []Option
	cursor  int
	chosen  map[int]bool
	value   *[]string
}

func NewMultiSelect(title string) *MultiSelect {
	return &MultiSelect{base: base{title: title}, chosen: map[int]bool{}}
}

func (s *MultiSelect) Description(d string) *MultiSelect    { s.desc = d; return s }
func (s *MultiSelect) Options(opts ...Option) *MultiSelect  { s.options = opts; return s }
func (s *MultiSelect) Value(v *[]string) *MultiSelect       { s.value = v; return s }
func (s *MultiSelect) WithHide(fn func() bool) *MultiSelect { s.hideFunc = fn; return s }

func (s *MultiSelect) Focus() tea.Cmd { return nil }
func (s *MultiSelect) Blur()          {}

func (s *MultiSelect) sync() {
	if s.value == nil {
		return
	}
	var out []string
	for i, o := range s.options {
		if s.chosen[i] {
			out = append(out, o.Value)
		}
	}
	*s.value = out
}

func (s *MultiSelect) Update(msg tea.Msg) (tea.Cmd, bool) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, false
	}
	switch key.String() {
	case "up", "k", "ctrl+p":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j", "ctrl+n":
		if s.cursor < len(s.options)-1 {
			s.cursor++
		}
	case "space", "x":
		s.chosen[s.cursor] = !s.chosen[s.cursor]
		s.sync()
	case "enter":
		s.sync()
		return nil, true
	}
	return nil, false
}

func (s *MultiSelect) View(width int) string {
	var b strings.Builder
	b.WriteString(s.header() + "\n")
	for i, o := range s.options {
		mark := "○"
		if s.chosen[i] {
			mark = "◉"
		}
		line := mark + " " + o.Label
		if i == s.cursor {
			b.WriteString(styles.Selected.Render("▸ "+line) + "\n")
		} else {
			b.WriteString(styles.Option.Render("  "+line) + "\n")
		}
	}
	b.WriteString(styles.Help.Render("space toggle · enter confirm"))
	return railed(strings.TrimRight(b.String(), "\n"))
}

// ── Input (single line, optional password echo) ──

type Input struct {
	base
	ti    textinput.Model
	value *string
}

func NewInput(title string) *Input {
	ti := textinput.New() // virtual cursor is the v2 default
	return &Input{base: base{title: title}, ti: ti}
}

func (s *Input) Description(d string) *Input    { s.desc = d; return s }
func (s *Input) Placeholder(p string) *Input    { s.ti.Placeholder = p; return s }
func (s *Input) Value(v *string) *Input         { s.value = v; return s }
func (s *Input) WithHide(fn func() bool) *Input { s.hideFunc = fn; return s }

// Password masks the echo.
func (s *Input) Password() *Input {
	s.ti.EchoMode = textinput.EchoPassword
	s.ti.EchoCharacter = '*'
	return s
}

func (s *Input) Focus() tea.Cmd {
	if s.value != nil {
		s.ti.SetValue(*s.value)
		s.ti.CursorEnd()
	}
	return s.ti.Focus()
}
func (s *Input) Blur() { s.ti.Blur() }

func (s *Input) Update(msg tea.Msg) (tea.Cmd, bool) {
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "enter" {
		if s.value != nil {
			*s.value = s.ti.Value()
		}
		return nil, true
	}
	var cmd tea.Cmd
	s.ti, cmd = s.ti.Update(msg)
	if s.value != nil {
		*s.value = s.ti.Value()
	}
	return cmd, false
}

func (s *Input) View(width int) string {
	var b strings.Builder
	b.WriteString(s.header() + "\n")
	b.WriteString(s.ti.View() + "\n")
	b.WriteString(styles.Help.Render("enter submit"))
	return railed(strings.TrimRight(b.String(), "\n"))
}

// ── Text (multiline) ──

type Text struct {
	base
	ta    textarea.Model
	value *string
}

func NewText(title string) *Text {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.SetHeight(8)
	// Clear the v2 default cursor-line background (harsh black block).
	st := ta.Styles()
	st.Focused.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(st)
	return &Text{base: base{title: title}, ta: ta}
}

func (s *Text) Description(d string) *Text    { s.desc = d; return s }
func (s *Text) Value(v *string) *Text         { s.value = v; return s }
func (s *Text) WithHide(fn func() bool) *Text { s.hideFunc = fn; return s }

func (s *Text) Focus() tea.Cmd {
	if s.value != nil {
		s.ta.SetValue(*s.value)
		s.ta.MoveToEnd()
	}
	return s.ta.Focus()
}
func (s *Text) Blur() { s.ta.Blur() }

func (s *Text) Update(msg tea.Msg) (tea.Cmd, bool) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "enter":
			if s.value != nil {
				*s.value = s.ta.Value()
			}
			return nil, true
		case "alt+enter", "ctrl+j":
			s.ta.InsertString("\n")
			if s.value != nil {
				*s.value = s.ta.Value()
			}
			return nil, false
		}
	}
	var cmd tea.Cmd
	s.ta, cmd = s.ta.Update(msg)
	if s.value != nil {
		*s.value = s.ta.Value()
	}
	return cmd, false
}

func (s *Text) View(width int) string {
	if width > 10 {
		s.ta.SetWidth(width - 4)
	}
	var b strings.Builder
	b.WriteString(s.header() + "\n")
	b.WriteString(s.ta.View() + "\n")
	b.WriteString(styles.Help.Render("ctrl+j / alt+enter newline · enter submit"))
	return railed(strings.TrimRight(b.String(), "\n"))
}

// ── Confirm ──

type Confirm struct {
	base
	yes         bool
	affirmative string
	negative    string
	value       *bool
}

func NewConfirm(title string) *Confirm {
	return &Confirm{base: base{title: title}, affirmative: "yes", negative: "no"}
}

func (s *Confirm) Description(d string) *Confirm    { s.desc = d; return s }
func (s *Confirm) Value(v *bool) *Confirm           { s.value = v; return s }
func (s *Confirm) WithHide(fn func() bool) *Confirm { s.hideFunc = fn; return s }
func (s *Confirm) Affirmative(a string) *Confirm    { s.affirmative = a; return s }
func (s *Confirm) Negative(n string) *Confirm       { s.negative = n; return s }

func (s *Confirm) Focus() tea.Cmd { return nil }
func (s *Confirm) Blur()          {}

func (s *Confirm) sync() {
	if s.value != nil {
		*s.value = s.yes
	}
}

func (s *Confirm) Update(msg tea.Msg) (tea.Cmd, bool) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil, false
	}
	switch key.String() {
	case "left", "right", "h", "l", "tab":
		s.yes = !s.yes
		s.sync()
	case "y":
		s.yes = true
		s.sync()
		return nil, true
	case "n":
		s.yes = false
		s.sync()
		return nil, true
	case "enter":
		s.sync()
		return nil, true
	}
	return nil, false
}

func (s *Confirm) View(width int) string {
	yesLabel, noLabel := "  "+s.affirmative+"  ", "  "+s.negative+"  "
	if s.yes {
		yesLabel = styles.Selected.Render("▸ " + s.affirmative + " ◂")
		noLabel = styles.Option.Render(noLabel)
	} else {
		noLabel = styles.Selected.Render("▸ " + s.negative + " ◂")
		yesLabel = styles.Option.Render(yesLabel)
	}
	var b strings.Builder
	b.WriteString(s.header() + "\n")
	b.WriteString(yesLabel + "   " + noLabel + "\n")
	b.WriteString(styles.Help.Render("←/→ toggle · y/n · enter confirm"))
	return railed(strings.TrimRight(b.String(), "\n"))
}
