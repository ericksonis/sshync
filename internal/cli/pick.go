package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericksonis/sshync/internal/store"
	"github.com/spf13/cobra"
)

func pickCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "pick [filter]",
		Aliases: []string{"ui"},
		Short:   "Interactive host picker: fuzzy search, connect, edit, toggle (default when run with no arguments)",
		Long: `Interactive host picker.

  /        filter (fuzzy)          enter  connect (ssh <alias>)
  e        edit the host's file    a      add a host
  i        toggle IdentitiesOnly   F      toggle ForwardAgent
  x        delete (asks first)     s      sync with the remote
  ?        all keys                q      quit`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !a.interactive {
				return errors.New("the picker needs a terminal; use `sshync list` instead")
			}
			filter := ""
			if len(args) == 1 {
				filter = args[0]
			}
			return runPicker(a, filter)
		},
	}
}

type hostItem struct{ e *store.Entry }

func (h hostItem) Title() string {
	if h.e.Scope == store.Local {
		return h.e.Alias + "  (local)"
	}
	return h.e.Alias
}

func (h hostItem) Description() string {
	b := h.e.Block
	dest := b.First("HostName")
	if u := b.First("User"); u != "" {
		dest = u + "@" + dest
	}
	if p := b.First("Port"); p != "" {
		dest += ":" + p
	}
	if k := shortKey(strings.Join(b.Get("IdentityFile"), ",")); k != "" {
		dest += "  ·  " + k
	}
	return dest
}

func (h hostItem) FilterValue() string {
	b := h.e.Block
	return h.e.Alias + " " + b.First("HostName") + " " + b.First("User")
}

type pickKeys struct {
	connect, edit, add, del, idOnly, fwdAgent, sync key.Binding
}

func newPickKeys() pickKeys {
	return pickKeys{
		connect:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "connect")),
		edit:     key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		add:      key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		del:      key.NewBinding(key.WithKeys("x", "delete"), key.WithHelp("x", "delete")),
		idOnly:   key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "IdentitiesOnly")),
		fwdAgent: key.NewBinding(key.WithKeys("F"), key.WithHelp("F", "ForwardAgent")),
		sync:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sync")),
	}
}

type picker struct {
	a       *app
	list    list.Model
	keys    pickKeys
	log     *bytes.Buffer // a.out/a.errOut while the picker owns the screen
	width   int
	height  int
	confirm *store.Entry // pending delete
	connect string       // alias chosen with enter
}

type (
	editedMsg struct {
		path string
		err  error
	}
	addedMsg  struct{ err error }
	syncedMsg struct {
		out string
		err error
	}
)

var (
	detailStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1).
			BorderForeground(lipgloss.AdaptiveColor{Light: "#A49FA5", Dark: "#5C5C5C"})
	pathStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#7D7D7D", Dark: "#8A8A8A"})
)

// newPicker redirects a's output into a buffer (shown as status messages)
// so command helpers don't scribble over the TUI.
func newPicker(a *app, filter string) *picker {
	m := &picker{a: a, keys: newPickKeys(), log: &bytes.Buffer{}}
	a.out, a.errOut = m.log, m.log
	m.list = list.New(nil, list.NewDefaultDelegate(), 0, 0)
	m.list.Title = "sshync"
	m.list.SetStatusBarItemName("host", "hosts")
	m.list.SetShowHelp(false) // drawn full-width under both panes in View
	m.list.StatusMessageLifetime *= 2
	short := func() []key.Binding {
		return []key.Binding{m.keys.connect, m.keys.edit, m.keys.idOnly, m.keys.fwdAgent}
	}
	m.list.AdditionalShortHelpKeys = short
	m.list.AdditionalFullHelpKeys = func() []key.Binding {
		return append(short(), m.keys.add, m.keys.del, m.keys.sync)
	}
	m.setItems()
	if filter != "" {
		m.list.SetFilterText(filter)
	}
	return m
}

func (m *picker) setItems() {
	hosts := m.a.st.Hosts()
	items := make([]list.Item, len(hosts))
	sortEntries(hosts)
	for i, e := range hosts {
		items[i] = hostItem{e}
	}
	m.list.SetItems(items)
}

func (m *picker) selected() *store.Entry {
	if it, ok := m.list.SelectedItem().(hostItem); ok {
		return it.e
	}
	return nil
}

// reload re-reads the store after a change and keeps the cursor on alias.
func (m *picker) reload(alias string) tea.Cmd {
	st, err := store.Open(m.a.paths)
	if err != nil {
		return m.status("reload failed: " + err.Error())
	}
	m.a.st = st
	m.setItems()
	for i, it := range m.list.Items() {
		if it.(hostItem).e.Alias == alias {
			m.list.Select(i)
			break
		}
	}
	return nil
}

// status shows msg, or failing that the last line written to the log.
func (m *picker) status(msg string) tea.Cmd {
	if msg == "" {
		lines := strings.Split(strings.TrimSpace(m.log.String()), "\n")
		msg = lines[len(lines)-1]
	}
	m.log.Reset()
	if msg == "" {
		return nil
	}
	return m.list.NewStatusMessage(msg)
}

func (m *picker) Init() tea.Cmd { return nil }

func (m *picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case editedMsg:
		if msg.err != nil {
			return m, m.status("editor: " + msg.err.Error())
		}
		m.a.changed(msg.path, "edit "+filepath.Base(msg.path))
		alias := ""
		if e := m.selected(); e != nil {
			alias = e.Alias
		}
		return m, tea.Batch(m.reload(alias), m.status("saved "+filepath.Base(msg.path)))
	case addedMsg:
		n := len(m.list.Items())
		cmd := m.reload("")
		if msg.err != nil {
			return m, tea.Batch(cmd, m.status("add: "+msg.err.Error()))
		}
		if len(m.list.Items()) > n {
			return m, tea.Batch(cmd, m.status("host added"))
		}
		return m, cmd
	case syncedMsg:
		alias := ""
		if e := m.selected(); e != nil {
			alias = e.Alias
		}
		cmd := m.reload(alias)
		if msg.err != nil {
			return m, tea.Batch(cmd, m.status("sync: "+firstLine(msg.err.Error())))
		}
		return m, tea.Batch(cmd, m.status("sync: "+strings.ReplaceAll(strings.TrimSpace(msg.out), "\n", "; ")))
	case tea.KeyMsg:
		if m.confirm != nil {
			e := m.confirm
			m.confirm = nil
			if msg.String() != "y" && msg.String() != "Y" {
				return m, m.status("kept " + e.Alias)
			}
			if err := m.a.st.Remove(e); err != nil {
				return m, m.status("delete: " + err.Error())
			}
			m.a.changed(e.Path, "remove "+e.Alias)
			return m, tea.Batch(m.reload(""), m.status("removed "+e.Alias))
		}
		if m.list.FilterState() == list.Filtering {
			break // typing goes to the filter
		}
		e := m.selected()
		switch {
		case key.Matches(msg, m.keys.connect) && e != nil:
			m.connect = e.Block.Patterns()[0]
			return m, tea.Quit
		case key.Matches(msg, m.keys.idOnly) && e != nil:
			return m, m.toggle(e, "IdentitiesOnly")
		case key.Matches(msg, m.keys.fwdAgent) && e != nil:
			return m, m.toggle(e, "ForwardAgent")
		case key.Matches(msg, m.keys.del) && e != nil:
			m.confirm = e
			return m, m.list.NewStatusMessage(fmt.Sprintf("delete %s? y/N", e.Alias))
		case key.Matches(msg, m.keys.edit) && e != nil:
			ed := editor()
			path := e.Path
			c := exec.Command(ed[0], append(ed[1:], path)...)
			return m, tea.ExecProcess(c, func(err error) tea.Msg { return editedMsg{path, err} })
		case key.Matches(msg, m.keys.add):
			exe, err := os.Executable()
			if err != nil {
				return m, m.status(err.Error())
			}
			return m, tea.ExecProcess(exec.Command(exe, "add"), func(err error) tea.Msg { return addedMsg{err} })
		case key.Matches(msg, m.keys.sync):
			r := m.a.repo()
			return m, tea.Batch(m.list.NewStatusMessage("syncing…"), func() tea.Msg {
				out, err := r.Sync("sshync: sync from "+hostname(), "")
				return syncedMsg{out, err}
			})
		}
	}
	var cmd tea.Cmd
	full := m.list.Help.ShowAll
	m.list, cmd = m.list.Update(msg)
	if m.list.Help.ShowAll != full {
		m.layout()
	}
	return m, cmd
}

// layout sizes the list to leave room for the full-width help bar.
func (m *picker) layout() {
	m.list.SetSize(m.listWidth(), m.height-lipgloss.Height(m.helpView()))
}

// helpView renders the list's help at full width (list.SetSize narrows Help.Width).
func (m *picker) helpView() string {
	h := m.list.Help
	h.Width = m.width
	return h.View(m.list)
}

func (m *picker) toggle(e *store.Entry, k string) tea.Cmd {
	v, changed, err := toggle(e, k, false, false)
	if err != nil {
		return m.status(err.Error())
	}
	if !changed {
		return nil
	}
	if err := m.a.st.Save(e); err != nil {
		return m.status(err.Error())
	}
	msg := fmt.Sprintf("%s: %s %s", e.Alias, k, v)
	m.a.changed(e.Path, msg)
	if strings.Contains(m.log.String(), "warning") {
		return m.status("")
	}
	return m.status(msg)
}

// listWidth leaves room for the detail pane on wide terminals.
func (m *picker) listWidth() int {
	if m.width >= 100 {
		return m.width * 2 / 5
	}
	return m.width
}

func (m *picker) View() string {
	help := m.helpView()
	left := lipgloss.NewStyle().MaxWidth(m.listWidth()).Render(m.list.View())
	w := m.width - m.listWidth()
	if w < 20 {
		return lipgloss.JoinVertical(lipgloss.Left, left, help)
	}
	detail := "no host selected"
	if e := m.selected(); e != nil {
		detail = pathStyle.Render(fmt.Sprintf("%s (%s)", m.shortPath(e.Path), e.Scope)) + "\n\n" +
			strings.TrimRight(e.Block.Text("\n"), "\n")
		if n := len(m.a.st.Find(e.Alias, "")); n > 1 {
			detail += "\n\n" + pathStyle.Render(fmt.Sprintf("defined %d times; see `sshync show %s`", n, e.Alias))
		}
	}
	right := detailStyle.Width(w - 4).MaxHeight(m.height - lipgloss.Height(help)).Render(detail)
	return lipgloss.JoinVertical(lipgloss.Left, lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right), help)
}

// shortPath shows files under the ssh dir as ~/.ssh/...
func (m *picker) shortPath(p string) string {
	if rel, err := filepath.Rel(m.a.paths.SSHDir, p); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/.ssh/" + filepath.ToSlash(rel)
	}
	return p
}

func runPicker(a *app, filter string) error {
	out, errOut := a.out, a.errOut
	m := newPicker(a, filter)
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	a.out, a.errOut = out, errOut
	if err != nil {
		return err
	}
	if m.connect == "" {
		return nil
	}
	return runSSH(a, m.connect)
}

// runSSH hands the terminal to ssh and passes its exit status through.
func runSSH(a *app, alias string) error {
	args := []string{alias}
	if os.Getenv("SSHYNC_SSH_DIR") != "" {
		args = append([]string{"-F", a.paths.Config}, args...)
	}
	c := exec.Command("ssh", args...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := c.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return exitCode(ee.ExitCode())
	}
	return err
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func sortEntries(es []*store.Entry) {
	sort.SliceStable(es, func(i, j int) bool { return strings.ToLower(es[i].Alias) < strings.ToLower(es[j].Alias) })
}
