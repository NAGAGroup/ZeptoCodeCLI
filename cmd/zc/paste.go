// Client-side input helpers that never round-trip to the server per keystroke:
//
//   - Paste placeholder registry: large pastes are stashed client-side and a
//     compact "[Pasted text #N +M lines]" placeholder is inserted into the
//     input. On submit the placeholder is resolved back to the full content.
//   - "@" file-path completion: typing "@" offers a filesystem completion
//     popup resolved against the SERVER cwd (m.serverCWD), one path segment at
//     a time (shell-style).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// ── paste placeholder registry ──

// largePasteLines / largePasteChars gate whether a paste is shown as a
// placeholder (large) or inserted inline as normal text (small).
const (
	largePasteLines = 5
	largePasteChars = 500
)

// isLargePaste reports whether pasted content should be represented by a
// placeholder rather than inserted inline.
func isLargePaste(content string) bool {
	return strings.Count(content, "\n")+1 > largePasteLines || len(content) > largePasteChars
}

// allocatePaste stores content in the client-side registry and returns the
// placeholder text to insert into the input. The registry is keyed by the
// placeholder string so resolvePastes can do a straight substring replace.
func (m *model) allocatePaste(content string) string {
	if m.pastes == nil {
		m.pastes = map[string]string{}
	}
	m.pasteSeq++
	lines := strings.Count(content, "\n") + 1
	placeholder := fmt.Sprintf("[Pasted text #%d +%d lines]", m.pasteSeq, lines)
	m.pastes[placeholder] = content
	return placeholder
}

// resolvePastes expands any placeholders in text back to their stored content.
func (m *model) resolvePastes(text string) string {
	for placeholder, content := range m.pastes {
		text = strings.ReplaceAll(text, placeholder, content)
	}
	return text
}

// clearPastes drops the registry after a submit so ids restart from #1.
func (m *model) clearPastes() {
	m.pastes = nil
	m.pasteSeq = 0
}

// handlePaste inserts pasted content at the cursor: large pastes become a
// placeholder (full content stashed in the registry); small pastes go inline.
func (m *model) handlePaste(content string) {
	if content == "" {
		return
	}
	if isLargePaste(content) {
		m.input.InsertString(m.allocatePaste(content))
	} else {
		m.input.InsertString(content)
	}
	m.autosize()
}

// ── "@" file-path completion ──

// lineBeforeCursor returns the text of the current logical line up to the
// cursor column.
func (m *model) lineBeforeCursor() string {
	lines := strings.Split(m.input.Value(), "\n")
	row := m.input.Line()
	if row < 0 || row >= len(lines) {
		return ""
	}
	r := []rune(lines[row])
	col := m.input.Column()
	if col > len(r) {
		col = len(r)
	}
	if col < 0 {
		col = 0
	}
	return string(r[:col])
}

// currentAtToken returns the "@…" token immediately before the cursor (with
// the leading "@" stripped) when the input is positioned inside such a token.
func (m *model) currentAtToken() (partial string, ok bool) {
	line := m.lineBeforeCursor()
	at := strings.LastIndex(line, "@")
	if at < 0 {
		return "", false
	}
	tok := line[at:]
	if strings.ContainsAny(tok, " \t") {
		return "", false
	}
	return tok[1:], true
}

// completionItems lists filesystem entries under serverCWD matching the
// partial path, one segment at a time (dirs get a trailing "/"). The item id
// is the replacement path (relative, without the leading "@").
func (m *model) completionItems(partial string) []overlayItem {
	root := m.serverCWD
	if root == "" {
		return nil
	}
	dir, base := "", partial
	if i := strings.LastIndex(partial, "/"); i >= 0 {
		dir = partial[:i+1] // keep the trailing slash
		base = partial[i+1:]
	}
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		return nil
	}
	lb := strings.ToLower(base)
	var items []overlayItem
	for _, e := range entries {
		name := e.Name()
		if base == "" {
			if strings.HasPrefix(name, ".") {
				continue // hide dotfiles until explicitly typed
			}
		} else if !strings.HasPrefix(strings.ToLower(name), lb) {
			continue
		}
		full := dir + name
		title := name
		if e.IsDir() {
			full += "/"
			title += "/"
		}
		items = append(items, overlayItem{id: full, title: title})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].title < items[j].title })
	if len(items) > 100 {
		items = items[:100]
	}
	return items
}

// refreshCompletion opens/updates/closes the "@" completion popup based on the
// token currently under the cursor.
func (m *model) refreshCompletion() {
	partial, ok := m.currentAtToken()
	if !ok {
		m.complete = nil
		return
	}
	items := m.completionItems(partial)
	if len(items) == 0 {
		m.complete = nil
		return
	}
	m.completeTok = "@" + partial
	m.complete = &overlay{kind: overlayPalette, title: "Files (" + m.completeTok + ")", items: items}
	m.complete.clampSel()
}

// cursorOffset returns the cursor position as a rune offset into the full
// input value.
func (m *model) cursorOffset() int {
	lines := strings.Split(m.input.Value(), "\n")
	row := m.input.Line()
	off := 0
	for i := 0; i < row && i < len(lines); i++ {
		off += len([]rune(lines[i])) + 1 // +1 for the newline
	}
	off += m.input.Column()
	return off
}

// setCursorOffset moves the textarea cursor to a rune offset in val.
func (m *model) setCursorOffset(val string, off int) {
	r := []rune(val)
	if off > len(r) {
		off = len(r)
	}
	if off < 0 {
		off = 0
	}
	row, col := 0, 0
	for i := 0; i < off; i++ {
		if r[i] == '\n' {
			row++
			col = 0
		} else {
			col++
		}
	}
	m.input.MoveToBegin()
	for i := 0; i < row; i++ {
		m.input.CursorDown()
	}
	m.input.SetCursorColumn(col)
}

// applyCompletion replaces the "@…" token before the cursor with the chosen
// path. Completing a directory keeps the popup open for the next segment;
// completing a file closes it.
func (m *model) applyCompletion(chosen string) {
	val := []rune(m.input.Value())
	end := m.cursorOffset()
	start := end - len([]rune(m.completeTok))
	if start < 0 || start > len(val) || end > len(val) {
		return
	}
	newTok := "@" + chosen
	newVal := string(val[:start]) + newTok + string(val[end:])
	m.input.SetValue(newVal)
	m.setCursorOffset(newVal, start+len([]rune(newTok)))
	m.autosize()

	if strings.HasSuffix(chosen, "/") {
		m.refreshCompletion() // descend into the directory
		return
	}
	m.complete = nil
}

// handleCompleteKey routes keys while the "@" completion popup is open.
func (m *model) handleCompleteKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m.handleCtrlC()
	case "esc":
		m.complete = nil
		return m, nil
	case "up", "ctrl+p":
		m.complete.move(-1)
		return m, nil
	case "down", "ctrl+n":
		m.complete.move(1)
		return m, nil
	case "enter", "tab":
		if it, ok := m.complete.current(); ok {
			m.applyCompletion(it.id)
		}
		return m, nil
	}
	// Any other key edits the buffer; re-evaluate the token afterward.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.autosize()
	m.refreshCompletion()
	return m, cmd
}
