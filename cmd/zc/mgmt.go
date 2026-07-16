// Management slash commands: /secret /skills /connect /memory
// /memory-repository /mods /hooks /mcp /profiles /crons.
//
// Wire-backed where the protocol allows (secrets, connect providers,
// memory suite, crons, skill enable/disable); filesystem-backed where
// 0.28.8 has no wire surface (skill listing — current_available_skills is
// hardcoded [] server-side; mods; hooks; profiles). zc runs on the same
// machine as the app-server, so local reads mirror what the server sees.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/NAGAGroup/ZeptoCodeCLI/internal/protocol"
	"github.com/NAGAGroup/ZeptoCodeCLI/internal/ui/form"
)

// ── mgmt form: a form in the modal slot with a completion callback ──

type mgmtForm struct {
	form   *form.Form
	title  string
	inited bool
	onDone func(m *model) tea.Cmd // runs when the form completes
}

// finishMgmtIfDone submits the form result once it reports completion.
func (m *model) finishMgmtIfDone() tea.Cmd {
	if m.mgmt == nil || m.mgmt.form.State() != form.StateCompleted {
		return nil
	}
	mf := m.mgmt
	m.mgmt = nil
	m.layout()
	if mf.onDone == nil {
		return nil
	}
	cmd := mf.onDone(m)
	m.refreshViewport()
	return cmd
}

// mgmtMsg is the generic async result for management operations.
type mgmtMsg struct {
	err     error
	errWrap string       // prefix for err
	entry   *entry       // append to transcript
	overlay *overlay     // open an overlay
	form    *mgmtForm    // open a form (Init dispatched by the handler)
	infoTxt string       // append entryInfo
}

func (m *model) applyMgmtMsg(msg mgmtMsg) tea.Cmd {
	if msg.err != nil {
		text := msg.err.Error()
		if msg.errWrap != "" {
			text = msg.errWrap + ": " + text
		}
		m.appendEntry(&entry{kind: entryError, text: text})
		m.refreshViewport()
		return nil
	}
	if msg.infoTxt != "" {
		m.appendEntry(&entry{kind: entryInfo, text: msg.infoTxt})
		m.refreshViewport()
	}
	if msg.entry != nil {
		m.closeStreaming()
		m.appendEntry(msg.entry)
		m.refreshViewport()
	}
	if msg.overlay != nil {
		m.overlay = msg.overlay
		m.layout()
	}
	if msg.form != nil {
		msg.form.inited = true
		m.mgmt = msg.form
		m.layout()
		return msg.form.form.Init()
	}
	return nil
}

func mgmtErr(prefix string, err error) tea.Cmd {
	return func() tea.Msg { return mgmtMsg{err: err, errWrap: prefix} }
}

// ── /secret ──

func cmdSecret(m *model, args string) tea.Cmd {
	fields := strings.Fields(args)
	cli := m.cli
	switch {
	case len(fields) == 0 || fields[0] == "list":
		return func() tea.Msg {
			secrets, err := cli.SecretList(context.Background())
			if err != nil {
				return mgmtMsg{err: err, errWrap: "/secret"}
			}
			items := make([]overlayItem, 0, len(secrets))
			for _, s := range secrets {
				items = append(items, overlayItem{
					id:    s.Key,
					title: s.Key,
					desc:  fmt.Sprintf("••• (%d chars) — enter edits", len(s.Value)),
				})
			}
			if len(items) == 0 {
				return mgmtMsg{infoTxt: "no secrets set — /secret set KEY [VALUE]"}
			}
			ov := &overlay{kind: overlayMgmt, title: "secrets (values never shown in transcript)", items: items}
			ov.onSelect = func(m *model, it overlayItem) tea.Cmd {
				return openSecretEditForm(m, it.id)
			}
			return mgmtMsg{overlay: ov}
		}
	case fields[0] == "set" && len(fields) >= 3:
		key, value := strings.ToUpper(fields[1]), strings.Join(fields[2:], " ")
		return applySecret(m, map[string]string{key: value}, nil, "set "+key)
	case fields[0] == "set" && len(fields) == 2:
		return openSecretEditForm(m, strings.ToUpper(fields[1]))
	case fields[0] == "unset" && len(fields) == 2:
		key := strings.ToUpper(fields[1])
		return openConfirmForm(m, "unset secret "+key+"?", func(m *model) tea.Cmd {
			return applySecret(m, nil, []string{key}, "unset "+key)
		})
	default:
		m.appendEntry(&entry{kind: entryError, text: "usage: /secret [list] | set KEY [VALUE] | unset KEY"})
		return nil
	}
}

// openSecretEditForm fetches the current value (prefill) and opens a
// password-echo input — the only surface where a value is visible.
func openSecretEditForm(m *model, key string) tea.Cmd {
	cli := m.cli
	return func() tea.Msg {
		current := ""
		if secrets, err := cli.SecretList(context.Background()); err == nil {
			for _, s := range secrets {
				if s.Key == key {
					current = s.Value
					break
				}
			}
		}
		val := current
		mf := &mgmtForm{title: "secret: " + key}
		mf.form = form.New(
			form.NewInput(key).
				Description("value hidden; submit empty to keep unchanged").
				Password().
				Value(&val),
		)
		mf.onDone = func(m *model) tea.Cmd {
			if val == "" || val == current {
				m.appendEntry(&entry{kind: entryInfo, text: "secret " + key + " unchanged"})
				return nil
			}
			return applySecret(m, map[string]string{key: val}, nil, "set "+key)
		}
		return mgmtMsg{form: mf}
	}
}

func applySecret(m *model, set map[string]string, unset []string, what string) tea.Cmd {
	cli := m.cli
	return func() tea.Msg {
		names, err := cli.SecretApply(context.Background(), set, unset)
		if err != nil {
			return mgmtMsg{err: err, errWrap: "/secret"}
		}
		return mgmtMsg{infoTxt: fmt.Sprintf("secret %s ✓ (%d secrets: %s)",
			what, len(names), compactOneLine(strings.Join(names, ", "), 80))}
	}
}

// openConfirmForm shows a yes/no confirm; onYes runs on confirmation.
func openConfirmForm(m *model, title string, onYes func(m *model) tea.Cmd) tea.Cmd {
	ok := false
	mf := &mgmtForm{title: "confirm"}
	mf.form = form.New(
		form.NewConfirm(title).Affirmative("yes").Negative("no").Value(&ok),
	)
	mf.onDone = func(m *model) tea.Cmd {
		if !ok {
			m.appendEntry(&entry{kind: entryInfo, text: "cancelled"})
			return nil
		}
		return onYes(m)
	}
	return func() tea.Msg { return mgmtMsg{form: mf} }
}

// ── /skills ──
//
// Listing is filesystem-based (no wire surface in 0.28.8). Sources:
// global ~/.letta/skills (symlinks), agent <memfs>/skills, project
// <server-cwd>/.skills and .agents/skills, bundled (letta package).

type skillInfo struct {
	name, source, path, desc string
	symlink                  bool
}

func cmdSkills(m *model, args string) tea.Cmd {
	fields := strings.Fields(args)
	cli := m.cli
	memoryDir := m.memoryDir
	serverCWD := m.serverCWD
	switch {
	case len(fields) == 0 || fields[0] == "list":
		return func() tea.Msg {
			skills := scanSkills(memoryDir, serverCWD)
			if len(skills) == 0 {
				return mgmtMsg{infoTxt: "no skills found (global/agent/project/bundled)"}
			}
			items := make([]overlayItem, 0, len(skills))
			for _, s := range skills {
				items = append(items, overlayItem{
					id:    s.source + "\x00" + s.name + "\x00" + s.path,
					title: fmt.Sprintf("%s  [%s]", s.name, s.source),
					desc:  compactOneLine(s.desc, 100),
				})
			}
			ov := &overlay{kind: overlayMgmt, title: "skills (enter shows detail)", items: items}
			ov.onSelect = func(m *model, it overlayItem) tea.Cmd {
				parts := strings.SplitN(it.id, "\x00", 3)
				if len(parts) != 3 {
					return nil
				}
				source, name, path := parts[0], parts[1], parts[2]
				text := fmt.Sprintf("skill: %s\nsource: %s\npath: %s", name, source, path)
				if source == "global" {
					text += "\n\ndisable with: /skills disable " + name
				} else if source != "bundled" {
					text += "\n\nenable globally with: /skills enable " + path
				}
				m.appendEntry(&entry{kind: entryInfo, text: text})
				m.refreshViewport()
				return nil
			}
			return mgmtMsg{overlay: ov}
		}
	case fields[0] == "enable" && len(fields) >= 2:
		path := strings.Join(fields[1:], " ")
		if strings.HasPrefix(path, "~") {
			if home, err := os.UserHomeDir(); err == nil {
				path = home + strings.TrimPrefix(path, "~")
			}
		}
		if !filepath.IsAbs(path) && serverCWD != "" {
			path = filepath.Join(serverCWD, path)
		}
		return func() tea.Msg {
			name, err := cli.SkillEnable(context.Background(), path)
			if err != nil {
				return mgmtMsg{err: err, errWrap: "/skills enable"}
			}
			return mgmtMsg{infoTxt: "skill enabled: " + name + " (symlinked into ~/.letta/skills)"}
		}
	case fields[0] == "disable" && len(fields) == 2:
		name := fields[1]
		return func() tea.Msg {
			out, err := cli.SkillDisable(context.Background(), name)
			if err != nil {
				return mgmtMsg{err: err, errWrap: "/skills disable"}
			}
			if out == "" {
				out = name
			}
			return mgmtMsg{infoTxt: "skill disabled: " + out}
		}
	default:
		m.appendEntry(&entry{kind: entryError, text: "usage: /skills [list] | enable <path> | disable <name>"})
		return nil
	}
}

func scanSkills(memoryDir, serverCWD string) []skillInfo {
	var out []skillInfo
	seen := map[string]bool{}
	add := func(dir, source string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			info, err := os.Stat(p) // follows symlinks
			if err != nil || !info.IsDir() {
				continue
			}
			skillMD := filepath.Join(p, "SKILL.md")
			if _, err := os.Stat(skillMD); err != nil {
				continue
			}
			key := source + "/" + e.Name()
			if seen[key] {
				continue
			}
			seen[key] = true
			lst, _ := os.Lstat(p)
			out = append(out, skillInfo{
				name:    e.Name(),
				source:  source,
				path:    p,
				desc:    skillDescription(skillMD),
				symlink: lst != nil && lst.Mode()&os.ModeSymlink != 0,
			})
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".letta", "skills"), "global")
	}
	if memoryDir != "" {
		add(filepath.Join(memoryDir, "skills"), "agent")
	}
	if serverCWD != "" {
		add(filepath.Join(serverCWD, ".skills"), "project")
		add(filepath.Join(serverCWD, ".agents", "skills"), "project")
	}
	if bundled := bundledSkillsDir(); bundled != "" {
		add(bundled, "bundled")
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].source != out[b].source {
			return out[a].source < out[b].source
		}
		return out[a].name < out[b].name
	})
	return out
}

// bundledSkillsDir locates the letta-code package's skills dir by resolving
// the `letta` binary (npm-style bin symlink → package letta.js).
func bundledSkillsDir() string {
	bin, err := exec.LookPath("letta")
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return ""
	}
	dir := filepath.Join(filepath.Dir(resolved), "skills")
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}

// skillDescription extracts `description:` from SKILL.md frontmatter.
func skillDescription(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "description:") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "description:")), `"'`)
		}
		if trimmed == "---" && line != strings.Split(string(raw), "\n")[0] {
			break // end of frontmatter without a description
		}
	}
	return ""
}

// ── /connect ──

func cmdConnect(m *model, args string) tea.Cmd {
	cli := m.cli
	if strings.TrimSpace(args) == "usage" {
		return func() tea.Msg {
			usage, err := cli.ChatGPTUsage(context.Background(), true)
			if err != nil {
				return mgmtMsg{err: err, errWrap: "/connect usage"}
			}
			var b strings.Builder
			b.WriteString("ChatGPT usage (" + usage.ProviderName + ")\n" + usage.Summary)
			if usage.PlanType != nil {
				b.WriteString("\nplan: " + *usage.PlanType)
			}
			for _, w := range append([]protocol.ChatGPTUsageWindow{}, usage.Additional...) {
				b.WriteString("\n" + formatUsageWindow(&w))
			}
			if usage.Primary != nil {
				b.WriteString("\n" + formatUsageWindow(usage.Primary))
			}
			if usage.Secondary != nil {
				b.WriteString("\n" + formatUsageWindow(usage.Secondary))
			}
			return mgmtMsg{entry: &entry{kind: entryCommand, cmdInput: "/connect usage", text: b.String()}}
		}
	}
	return func() tea.Msg {
		providers, err := cli.ListConnectProviders(context.Background())
		if err != nil {
			return mgmtMsg{err: err, errWrap: "/connect"}
		}
		return mgmtMsg{overlay: providersOverlay(providers)}
	}
}

func formatUsageWindow(w *protocol.ChatGPTUsageWindow) string {
	pct := "?"
	if w.UsedPercent != nil {
		pct = fmt.Sprintf("%.0f%%", *w.UsedPercent)
	}
	reset := ""
	if w.ResetsAt != nil {
		reset = " · resets " + time.Unix(*w.ResetsAt, 0).Local().Format("Jan 2 15:04")
	}
	return fmt.Sprintf("%s: %s used%s", w.Label, pct, reset)
}

func providersOverlay(providers []protocol.ConnectProviderEntry) *overlay {
	items := make([]overlayItem, 0, len(providers))
	byID := map[string]protocol.ConnectProviderEntry{}
	for _, p := range providers {
		byID[p.ID] = p
		mark := ""
		connectedNames := []string{}
		for _, c := range p.ConnectedProviders {
			if c.IsConnected {
				connectedNames = append(connectedNames, c.ProviderName)
			}
		}
		if len(connectedNames) > 0 {
			mark = " ✓ connected"
		}
		if p.IsOAuth && len(p.AuthMethods) == 0 && len(p.Fields) == 0 {
			mark += "  (oauth — native CLI only)"
		}
		items = append(items, overlayItem{
			id:    p.ID,
			title: p.DisplayName + mark,
			desc:  compactOneLine(p.Description, 90),
		})
	}
	ov := &overlay{kind: overlayMgmt, title: "connect providers", items: items}
	ov.onSelect = func(m *model, it overlayItem) tea.Cmd {
		p, ok := byID[it.id]
		if !ok {
			return nil
		}
		return openProviderForm(m, p)
	}
	return ov
}

// openProviderForm builds the connect/disconnect form for one provider.
// OAuth-only providers get a hint (the wire path throws for OAuth —
// verified in 0.28.8 connect-provider-service.ts).
func openProviderForm(m *model, p protocol.ConnectProviderEntry) tea.Cmd {
	connected := false
	for _, c := range p.ConnectedProviders {
		if c.IsConnected {
			connected = true
			break
		}
	}
	if p.IsOAuth && len(p.AuthMethods) == 0 && len(p.Fields) == 0 && !connected {
		m.appendEntry(&entry{kind: entryInfo, text: p.DisplayName +
			" uses OAuth, which has no wire path — run `letta` (native CLI) and use /connect there."})
		m.refreshViewport()
		return nil
	}

	// Normalize to auth methods: entries without auth_methods carry a flat
	// fields list (single implicit method).
	methods := p.AuthMethods
	if len(methods) == 0 {
		methods = []protocol.ConnectProviderAuthMethod{{ID: "", Label: "API key", Fields: p.Fields}}
	}

	const actConnect, actDisconnect, actCancel = "connect", "disconnect", "cancel"
	action := actConnect
	methodChoice := "0"
	// Bound values: one slice per method, one string per field.
	values := make([][]string, len(methods))
	for i := range methods {
		values[i] = make([]string, len(methods[i].Fields))
	}

	var fields []form.Field
	if connected {
		fields = append(fields, form.NewSelect(p.DisplayName+" is connected").
			Options(
				form.Option{Label: "update credentials", Value: actConnect},
				form.Option{Label: "disconnect", Value: actDisconnect},
				form.Option{Label: "cancel", Value: actCancel},
			).Value(&action))
	}
	if len(methods) > 1 {
		var mopts []form.Option
		for i, am := range methods {
			label := am.Label
			if label == "" {
				label = am.ID
			}
			mopts = append(mopts, form.Option{Label: label, Value: strconv.Itoa(i)})
		}
		fields = append(fields, form.NewSelect("auth method").
			Options(mopts...).Value(&methodChoice).
			WithHide(func() bool { return action != actConnect }))
	}
	nInputs := 0
	for i := range methods {
		i := i
		for j := range methods[i].Fields {
			j := j
			f := methods[i].Fields[j]
			in := form.NewInput(f.Label).Value(&values[i][j]).
				WithHide(func() bool {
					return action != actConnect || methodChoice != strconv.Itoa(i)
				})
			if f.Placeholder != "" {
				in = in.Placeholder(f.Placeholder)
			}
			if f.Secret {
				in = in.Password()
			}
			fields = append(fields, in)
			nInputs++
		}
	}

	if len(fields) == 0 || (!connected && nInputs == 0) {
		m.appendEntry(&entry{kind: entryInfo, text: p.DisplayName + ": nothing to configure over the wire"})
		m.refreshViewport()
		return nil
	}

	cli := m.cli
	mf := &mgmtForm{title: "connect: " + p.DisplayName}
	mf.form = form.New(fields...)
	mf.onDone = func(m *model) tea.Cmd {
		switch action {
		case actCancel:
			m.appendEntry(&entry{kind: entryInfo, text: "cancelled"})
			return nil
		case actDisconnect:
			return func() tea.Msg {
				if _, err := cli.DisconnectProvider(context.Background(), p.ID, ""); err != nil {
					return mgmtMsg{err: err, errWrap: "/connect"}
				}
				return mgmtMsg{infoTxt: p.DisplayName + " disconnected (model availability may have changed — /model to refresh)"}
			}
		default:
			methodIdx, _ := strconv.Atoi(methodChoice)
			if methodIdx < 0 || methodIdx >= len(methods) {
				methodIdx = 0
			}
			am := methods[methodIdx]
			fieldMap := map[string]string{}
			for j, f := range am.Fields {
				if strings.TrimSpace(values[methodIdx][j]) != "" {
					fieldMap[f.Key] = strings.TrimSpace(values[methodIdx][j])
				}
			}
			if len(fieldMap) == 0 {
				m.appendEntry(&entry{kind: entryError, text: "no fields entered — not connecting"})
				return nil
			}
			return func() tea.Msg {
				if _, err := cli.ConnectProvider(context.Background(), p.ID, am.ID, fieldMap); err != nil {
					return mgmtMsg{err: err, errWrap: "/connect"}
				}
				return mgmtMsg{infoTxt: p.DisplayName + " connected ✓ (model availability may have changed — /model to refresh)"}
			}
		}
	}
	return func() tea.Msg { return mgmtMsg{form: mf} }
}

// ── /memory ──

func cmdMemory(m *model, args string) tea.Cmd {
	fields := strings.Fields(args)
	cli := m.cli
	switch {
	case len(fields) == 0 || fields[0] == "list":
		return func() tea.Msg {
			resp, err := cli.MemoryList(context.Background())
			if err != nil {
				return mgmtMsg{err: err, errWrap: "/memory"}
			}
			if resp.MemfsEnabled != nil && !*resp.MemfsEnabled {
				return mgmtMsg{infoTxt: "memfs is not enabled for this agent — /memory enable"}
			}
			entries := resp.Entries
			sort.Slice(entries, func(a, b int) bool { return entries[a].RelativePath < entries[b].RelativePath })
			items := make([]overlayItem, 0, len(entries))
			contents := map[string]protocol.MemoryFileEntry{}
			for _, e := range entries {
				contents[e.RelativePath] = e
				marker := ""
				if e.IsSystem {
					marker = " [system]"
				}
				items = append(items, overlayItem{
					id:    e.RelativePath,
					title: e.RelativePath + marker,
					desc:  compactOneLine(fmt.Sprintf("%dB  %s", e.Size, e.Description), 90),
				})
			}
			ov := &overlay{kind: overlayMgmt, title: fmt.Sprintf("memory files (%d) — enter views", len(items)), items: items}
			ov.onSelect = func(m *model, it overlayItem) tea.Cmd {
				e, ok := contents[it.id]
				if !ok {
					return nil
				}
				if e.Kind == "image" {
					m.appendEntry(&entry{kind: entryInfo, text: it.id + " is binary (" + e.Kind + ") — not rendering"})
					m.refreshViewport()
					return nil
				}
				m.appendEntry(&entry{kind: entryDoc, cmdInput: "memory: " + it.id, text: e.Content})
				m.refreshViewport()
				return nil
			}
			return mgmtMsg{overlay: ov}
		}
	case fields[0] == "view" && len(fields) == 2:
		path := fields[1]
		return func() tea.Msg {
			content, err := cli.MemoryRead(context.Background(), path)
			if err != nil {
				return mgmtMsg{err: err, errWrap: "/memory view"}
			}
			return mgmtMsg{entry: &entry{kind: entryDoc, cmdInput: "memory: " + path, text: content}}
		}
	case fields[0] == "write" && len(fields) == 2:
		path := fields[1]
		return func() tea.Msg {
			current, _ := cli.MemoryRead(context.Background(), path) // absent file ⇒ empty editor
			content := current
			mf := &mgmtForm{title: "edit " + path}
			mf.form = form.New(
				form.NewText(path).
					Description("edits commit to the agent's memory repo").
					Value(&content),
			)
			mf.onDone = func(m *model) tea.Cmd {
				if content == current {
					m.appendEntry(&entry{kind: entryInfo, text: path + " unchanged"})
					return nil
				}
				return func() tea.Msg {
					resp, err := cli.MemoryWrite(context.Background(), path, content, "zc: edit "+path)
					if err != nil {
						return mgmtMsg{err: err, errWrap: "/memory write"}
					}
					sha := resp.CommitSHA
					if len(sha) > 7 {
						sha = sha[:7]
					}
					return mgmtMsg{infoTxt: "wrote " + path + " (commit " + sha + ")"}
				}
			}
			return mgmtMsg{form: mf}
		}
	case fields[0] == "rm" && len(fields) == 2:
		path := fields[1]
		return openConfirmForm(m, "delete memory file "+path+"?", func(m *model) tea.Cmd {
			cli := m.cli
			return func() tea.Msg {
				if _, err := cli.MemoryDelete(context.Background(), path, "zc: delete "+path); err != nil {
					return mgmtMsg{err: err, errWrap: "/memory rm"}
				}
				return mgmtMsg{infoTxt: "deleted " + path}
			}
		})
	case fields[0] == "enable":
		return func() tea.Msg {
			dir, err := cli.EnableMemfs(context.Background())
			if err != nil {
				return mgmtMsg{err: err, errWrap: "/memory enable"}
			}
			return mgmtMsg{infoTxt: "memfs enabled: " + dir}
		}
	default:
		m.appendEntry(&entry{kind: entryError, text: "usage: /memory [list] | view <path> | write <path> | rm <path> | enable"})
		return nil
	}
}

// ── /memory-repository ──

func cmdMemoryRepository(m *model, args string) tea.Cmd {
	filePath := strings.TrimSpace(args)
	cli := m.cli
	return func() tea.Msg {
		commits, err := cli.MemoryHistory(context.Background(), filePath, 50)
		if err != nil {
			return mgmtMsg{err: err, errWrap: "/memory-repository"}
		}
		if len(commits) == 0 {
			return mgmtMsg{infoTxt: "no memory commits" + forFile(filePath)}
		}
		items := make([]overlayItem, 0, len(commits))
		for _, c := range commits {
			sha := c.SHA
			if len(sha) > 7 {
				sha = sha[:7]
			}
			when := c.Timestamp
			if t, err := time.Parse(time.RFC3339, c.Timestamp); err == nil {
				when = t.Local().Format("Jan 2 15:04")
			}
			author := c.AuthorName
			if author != "" {
				author = " · " + author
			}
			items = append(items, overlayItem{
				id:    c.SHA,
				title: sha + "  " + compactOneLine(c.Message, 70),
				desc:  when + author,
			})
		}
		ov := &overlay{kind: overlayMgmt, title: "memory history" + forFile(filePath) + " — enter shows diff", items: items}
		ov.onSelect = func(m *model, it overlayItem) tea.Cmd {
			cli := m.cli
			return func() tea.Msg {
				diff, err := cli.MemoryCommitDiff(context.Background(), it.id)
				if err != nil {
					return mgmtMsg{err: err, errWrap: "/memory-repository"}
				}
				sha := it.id
				if len(sha) > 7 {
					sha = sha[:7]
				}
				return mgmtMsg{entry: &entry{kind: entryDiff, cmdInput: "memory diff " + sha, text: diff}}
			}
		}
		return mgmtMsg{overlay: ov}
	}
}

func forFile(path string) string {
	if path == "" {
		return ""
	}
	return " for " + path
}

// ── /mods ──

func cmdMods(m *model, args string) tea.Cmd {
	if strings.TrimSpace(args) == "reload" {
		// `reload` is in SUPPORTED_REMOTE_COMMANDS — output arrives as
		// command_start/command_end stream deltas.
		m.closeStreaming()
		m.appendEntry(&entry{kind: entryUser, text: "/mods reload"})
		if err := m.cli.ExecuteCommand("reload", ""); err != nil {
			m.appendEntry(&entry{kind: entryError, text: "reload failed: " + err.Error()})
		}
		return nil
	}
	return func() tea.Msg {
		items, diagText := scanMods()
		if len(items) == 0 {
			return mgmtMsg{infoTxt: "no mods found under ~/.letta/mods"}
		}
		ov := &overlay{kind: overlayMgmt, title: "mods (" + diagText + ") — /mods reload applies changes", items: items}
		ov.onSelect = func(m *model, it overlayItem) tea.Cmd {
			m.appendEntry(&entry{kind: entryInfo, text: it.title + "\n" + it.desc})
			m.refreshViewport()
			return nil
		}
		return mgmtMsg{overlay: ov}
	}
}

func scanMods() ([]overlayItem, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "no home"
	}
	modsDir := filepath.Join(home, ".letta", "mods")
	var items []overlayItem

	// Top-level mod files.
	if entries, err := os.ReadDir(modsDir); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || (!strings.HasSuffix(name, ".ts") && !strings.HasSuffix(name, ".mjs") && !strings.HasSuffix(name, ".js")) {
				continue
			}
			items = append(items, overlayItem{
				id:    name,
				title: name,
				desc:  filepath.Join(modsDir, name),
			})
		}
	}

	// Installed packages.
	if raw, err := os.ReadFile(filepath.Join(modsDir, "packages.json")); err == nil {
		var pkgs struct {
			Packages []struct {
				Source  string `json:"source"`
				Version string `json:"version"`
				Enabled bool   `json:"enabled"`
				Root    string `json:"root"`
			} `json:"packages"`
		}
		if json.Unmarshal(raw, &pkgs) == nil {
			for _, p := range pkgs.Packages {
				state := "enabled"
				if !p.Enabled {
					state = "disabled"
				}
				items = append(items, overlayItem{
					id:    p.Source,
					title: fmt.Sprintf("📦 %s@%s (%s)", p.Source, p.Version, state),
					desc:  filepath.Join(modsDir, p.Root),
				})
			}
		}
	}

	// Diagnostics from the last mod load.
	diagText := "no diagnostics"
	if raw, err := os.ReadFile(filepath.Join(modsDir, "diagnostics", "latest.json")); err == nil {
		var diag struct {
			Report struct {
				Diagnostics  []map[string]any `json:"diagnostics"`
				ErrorCount   int              `json:"errorCount"`
				WarningCount int              `json:"warningCount"`
			} `json:"report"`
		}
		if json.Unmarshal(raw, &diag) == nil {
			diagText = fmt.Sprintf("last load: %d errors, %d warnings", diag.Report.ErrorCount, diag.Report.WarningCount)
			for _, d := range diag.Report.Diagnostics {
				b, _ := json.Marshal(d)
				items = append(items, overlayItem{
					id:    "diag",
					title: "⚠ diagnostic",
					desc:  compactOneLine(string(b), 120),
				})
			}
		}
	}
	return items, diagText
}

// ── /hooks ──

func cmdHooks(m *model, _ string) tea.Cmd {
	serverCWD := m.serverCWD
	return func() tea.Msg {
		text := renderHooksView(serverCWD)
		return mgmtMsg{entry: &entry{kind: entryCommand, cmdInput: "/hooks", text: text}}
	}
}

// renderHooksView reads every settings source the server consults and
// summarizes hooks per file. Edits require /reload to take effect (settings
// are cached in-process; no file watcher in 0.28.8).
func renderHooksView(serverCWD string) string {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".config")
		}
		paths = append(paths,
			filepath.Join(xdg, "letta", "settings.json"),
			filepath.Join(home, ".letta", "settings.json"),
		)
	}
	if serverCWD != "" {
		paths = append(paths,
			filepath.Join(serverCWD, ".letta", "settings.json"),
			filepath.Join(serverCWD, ".letta", "settings.local.json"),
		)
	}

	var b strings.Builder
	found := false
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var settings struct {
			Hooks map[string]json.RawMessage `json:"hooks"`
		}
		if json.Unmarshal(raw, &settings) != nil || len(settings.Hooks) == 0 {
			continue
		}
		found = true
		b.WriteString(p + "\n")
		var events []string
		for ev := range settings.Hooks {
			events = append(events, ev)
		}
		sort.Strings(events)
		for _, ev := range events {
			if ev == "disabled" {
				b.WriteString("  disabled: " + string(settings.Hooks[ev]) + "\n")
				continue
			}
			var matchers []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
					Prompt  string `json:"prompt"`
				} `json:"hooks"`
			}
			if json.Unmarshal(settings.Hooks[ev], &matchers) != nil {
				b.WriteString("  " + ev + ": (unparsed)\n")
				continue
			}
			for _, ma := range matchers {
				match := ma.Matcher
				if match == "" {
					match = "*"
				}
				for _, h := range ma.Hooks {
					detail := h.Command
					if h.Type == "prompt" {
						detail = compactOneLine(h.Prompt, 60)
					}
					b.WriteString(fmt.Sprintf("  %s [%s] %s: %s\n", ev, match, h.Type, compactOneLine(detail, 80)))
				}
			}
		}
		b.WriteString("\n")
	}
	if !found {
		b.WriteString("no hooks configured in any settings file\n\nsources checked:\n  " + strings.Join(paths, "\n  ") + "\n")
	}
	b.WriteString("\nhooks are cached in-process — run /reload (or /mods reload) after editing")
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// ── /mcp ──

func cmdMCP(m *model, _ string) tea.Cmd {
	m.appendEntry(&entry{kind: entryInfo, text: strings.TrimSpace(`
/mcp is not available in zc: MCP servers are managed on the Letta backend
via the SDK (client.mcpServers.*) — there is no wire command for MCP in the
app-server protocol (0.28.8), and the local backend does not implement MCP
management at all.

Options:
  · API backend: run the native letta TUI and use /mcp there
  · convert MCP servers to skills (converting-mcps-to-skills skill)`)})
	return nil
}

// ── /profiles ──
//
// Profiles are a name → agentId map under the "profiles" key in settings
// (global ~/.letta/settings.json + project .letta/settings.json, project
// wins). Purely local — no wire surface — so zc reads/writes the files.

func cmdProfiles(m *model, args string) tea.Cmd {
	fields := strings.Fields(args)
	serverCWD := m.serverCWD
	switch {
	case len(fields) == 0 || fields[0] == "list":
		return func() tea.Msg {
			profiles := loadProfiles(serverCWD)
			if len(profiles) == 0 {
				return mgmtMsg{infoTxt: "no profiles — /profiles save <name> maps a name to the current agent"}
			}
			var names []string
			for n := range profiles {
				names = append(names, n)
			}
			sort.Strings(names)
			items := make([]overlayItem, 0, len(names))
			for _, n := range names {
				items = append(items, overlayItem{id: profiles[n], title: n, desc: profiles[n]})
			}
			ov := &overlay{kind: overlayMgmt, title: "profiles — enter switches agent", items: items}
			ov.onSelect = func(m *model, it overlayItem) tea.Cmd {
				return m.switchAgent(it.id)
			}
			return mgmtMsg{overlay: ov}
		}
	case fields[0] == "save" && len(fields) == 2:
		name, agentID := fields[1], m.cli.Runtime.AgentID
		cli := m.cli
		return func() tea.Msg {
			if err := saveProfile(name, agentID); err != nil {
				return mgmtMsg{err: err, errWrap: "/profiles save"}
			}
			// The app-server caches settings in-process and persists that
			// cache on its own writes — an external edit gets clobbered
			// unless the server re-reads the file. /reload does exactly that.
			_ = cli.ExecuteCommand("reload", "")
			return mgmtMsg{infoTxt: "profile " + name + " → " + agentID + " (global settings; server reloaded)"}
		}
	// NB: "rm" is the primary alias — pasted input exactly matching a bound
	// key name ("delete") gets coalesced and eaten by the input layer.
	case (fields[0] == "rm" || fields[0] == "delete") && len(fields) == 2:
		name := fields[1]
		cli := m.cli
		return func() tea.Msg {
			if err := deleteProfile(name); err != nil {
				return mgmtMsg{err: err, errWrap: "/profiles rm"}
			}
			_ = cli.ExecuteCommand("reload", "")
			return mgmtMsg{infoTxt: "profile " + name + " deleted (global settings; server reloaded)"}
		}
	default:
		m.appendEntry(&entry{kind: entryError, text: "usage: /profiles [list] | save <name> | rm <name>"})
		return nil
	}
}

func globalSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".letta", "settings.json"), nil
}

func loadProfiles(serverCWD string) map[string]string {
	out := map[string]string{}
	read := func(path string) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return
		}
		var s struct {
			Profiles map[string]string `json:"profiles"`
		}
		if json.Unmarshal(raw, &s) == nil {
			for k, v := range s.Profiles {
				out[k] = v // later sources (project) override earlier (global)
			}
		}
	}
	if gp, err := globalSettingsPath(); err == nil {
		read(gp)
	}
	if serverCWD != "" {
		read(filepath.Join(serverCWD, ".letta", "settings.json"))
	}
	return out
}

// mutateGlobalProfiles read-modify-writes ONLY the profiles key, preserving
// every other settings key verbatim.
func mutateGlobalProfiles(mutate func(map[string]any)) error {
	path, err := globalSettingsPath()
	if err != nil {
		return err
	}
	settings := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	}
	profiles, _ := settings["profiles"].(map[string]any)
	if profiles == nil {
		profiles = map[string]any{}
	}
	mutate(profiles)
	settings["profiles"] = profiles
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

func saveProfile(name, agentID string) error {
	return mutateGlobalProfiles(func(p map[string]any) { p[name] = agentID })
}

func deleteProfile(name string) error {
	found := false
	err := mutateGlobalProfiles(func(p map[string]any) {
		if _, ok := p[name]; ok {
			found = true
			delete(p, name)
		}
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no global profile named %q (project-level profiles must be edited in the project settings file)", name)
	}
	return nil
}

// ── /crons ──

func cmdCrons(m *model, args string) tea.Cmd {
	fields := strings.Fields(args)
	cli := m.cli
	switch {
	case len(fields) == 0 || fields[0] == "list":
		return func() tea.Msg {
			tasks, err := cli.CronList(context.Background())
			if err != nil {
				return mgmtMsg{err: err, errWrap: "/crons"}
			}
			if len(tasks) == 0 {
				return mgmtMsg{infoTxt: "no cron tasks for this agent (letta cron add …)"}
			}
			items := make([]overlayItem, 0, len(tasks))
			byID := map[string]protocol.CronTask{}
			for _, t := range tasks {
				byID[t.ID] = t
				sched := t.Cron
				if !t.Recurring {
					sched = "one-shot"
					if t.ScheduledFor != nil {
						sched += " @ " + *t.ScheduledFor
					}
				}
				outcome := ""
				if t.LastRunOutcome != nil {
					outcome = " · last " + *t.LastRunOutcome
				}
				items = append(items, overlayItem{
					id:    t.ID,
					title: fmt.Sprintf("%s [%s] %s%s", t.Name, t.Status, sched, outcome),
					desc:  compactOneLine(t.Description, 90),
				})
			}
			ov := &overlay{kind: overlayMgmt, title: "cron tasks — enter shows detail + runs", items: items}
			ov.onSelect = func(m *model, it overlayItem) tea.Cmd {
				t, ok := byID[it.id]
				if !ok {
					return nil
				}
				cli := m.cli
				return func() tea.Msg {
					var b strings.Builder
					fmt.Fprintf(&b, "%s (%s)\nstatus: %s · fires: %d · missed: %d · failed: %d\nschedule: %s (%s) recurring=%v\nprompt: %s\n",
						t.Name, t.ID, t.Status, t.FireCount, t.MissedCount, t.FailedCount,
						t.Cron, t.Timezone, t.Recurring, compactOneLine(t.Prompt, 200))
					if page, err := cli.CronRuns(context.Background(), t.ID, 5); err == nil && page != nil && len(page.Entries) > 0 {
						b.WriteString("\nrecent runs:\n")
						for _, r := range page.Entries {
							when := time.UnixMilli(r.TS).Local().Format("Jan 2 15:04")
							line := fmt.Sprintf("  %s [%s] %s", when, r.Status, compactOneLine(r.Summary, 80))
							if r.Error != "" {
								line += " · " + compactOneLine(r.Error, 60)
							}
							b.WriteString(line + "\n")
						}
					}
					b.WriteString("\n/crons trigger " + t.Name + " · /crons rm " + t.Name)
					return mgmtMsg{entry: &entry{kind: entryCommand, cmdInput: "cron: " + t.Name, text: strings.TrimRight(b.String(), "\n")}}
				}
			}
			return mgmtMsg{overlay: ov}
		}
	case fields[0] == "trigger" && len(fields) == 2:
		ref := fields[1]
		return func() tea.Msg {
			id, err := resolveCronRef(cli, ref)
			if err != nil {
				return mgmtMsg{err: err, errWrap: "/crons trigger"}
			}
			if err := cli.CronTrigger(context.Background(), id); err != nil {
				return mgmtMsg{err: err, errWrap: "/crons trigger"}
			}
			return mgmtMsg{infoTxt: "cron triggered: " + ref}
		}
	case (fields[0] == "rm" || fields[0] == "delete") && len(fields) == 2:
		ref := fields[1]
		return openConfirmForm(m, "delete cron "+ref+"?", func(m *model) tea.Cmd {
			cli := m.cli
			return func() tea.Msg {
				id, err := resolveCronRef(cli, ref)
				if err != nil {
					return mgmtMsg{err: err, errWrap: "/crons rm"}
				}
				if err := cli.CronDelete(context.Background(), id); err != nil {
					return mgmtMsg{err: err, errWrap: "/crons rm"}
				}
				return mgmtMsg{infoTxt: "cron deleted: " + ref}
			}
		})
	default:
		m.appendEntry(&entry{kind: entryError, text: "usage: /crons [list] | trigger <name|id> | rm <name|id>"})
		return nil
	}
}

// resolveCronRef accepts a task id or a (unique) task name.
func resolveCronRef(cli interface {
	CronList(ctx context.Context) ([]protocol.CronTask, error)
}, ref string) (string, error) {
	tasks, err := cli.CronList(context.Background())
	if err != nil {
		return "", err
	}
	var matches []string
	for _, t := range tasks {
		if t.ID == ref {
			return t.ID, nil
		}
		if t.Name == ref {
			matches = append(matches, t.ID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no cron task named %q", ref)
	default:
		return "", fmt.Errorf("cron name %q is ambiguous (%d tasks) — use the id", ref, len(matches))
	}
}
