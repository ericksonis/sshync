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

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericksonis/sshync/internal/store"
	"github.com/spf13/cobra"
)

func pickCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "pick [search]",
		Aliases: []string{"ui"},
		Short:   "Interactive host table: search, connect, edit (default when run with no arguments)",
		Long: `Interactive host table. Type to search alias, hostname, user and key; every
space-separated term must match.

  up/down pgup/pgdn home/end   move          enter   connect (ssh <alias>)
  tab     show/hide the config pane          ctrl+o  full-screen config
  ctrl+e  edit the host's file               ctrl+n  add a host
  ctrl+d  delete (asks first)                ctrl+s  sync with the remote
  esc     clear the search, then quit        ctrl+c  quit`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !a.interactive {
				return errors.New("the picker needs a terminal; use `sshync list` instead")
			}
			search := ""
			if len(args) == 1 {
				search = args[0]
			}
			return runPicker(a, search)
		},
	}
}

var pickColumns = []struct {
	title string
	max   int
}{{"Alias", 32}, {"User", 16}, {"HostName", 40}, {"Port", 5}, {"Key", 28}, {"Scope", 6}}

func hostRow(e *store.Entry) table.Row {
	b := e.Block
	return table.Row{e.Alias, b.First("User"), b.First("HostName"), b.First("Port"),
		shortKey(strings.Join(b.Get("IdentityFile"), ",")), string(e.Scope)}
}

type picker struct {
	a        *app
	search   textinput.Model
	table    table.Model
	all      []*store.Entry
	shown    []*store.Entry // all, filtered by search, in table order
	log      *bytes.Buffer  // a.out/a.errOut while the picker owns the screen
	width    int
	height   int
	pane     bool         // config pane under the table
	expanded bool         // full-screen config
	confirm  *store.Entry // pending delete
	msg      string       // status line; replaces the help until the next key
	connect  string       // alias chosen with enter
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
	grey        = lipgloss.AdaptiveColor{Light: "#8A8A8A", Dark: "#6C6C6C"}
	dimStyle    = lipgloss.NewStyle().Foreground(grey)
	titleStyle  = lipgloss.NewStyle().Bold(true).Padding(0, 1).Foreground(lipgloss.Color("#FFFDF5")).Background(lipgloss.Color("#5A56E0"))
	detailStyle = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(grey).Padding(0, 1)
)

// newPicker redirects a's output into a buffer (shown on the status line)
// so command helpers don't scribble over the TUI.
func newPicker(a *app, search string) *picker {
	m := &picker{a: a, log: &bytes.Buffer{}, pane: true}
	a.out, a.errOut = m.log, m.log
	m.search = textinput.New()
	m.search.Prompt = "> "
	m.search.Placeholder = "search"
	m.search.SetValue(search)
	m.search.Focus()
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).BorderStyle(lipgloss.NormalBorder()).BorderBottom(true).BorderForeground(grey)
	s.Selected = s.Selected.Bold(false).Foreground(lipgloss.Color("#FFFDF5")).Background(lipgloss.Color("#5A56E0"))
	m.table = table.New(table.WithFocused(true), table.WithStyles(s))
	m.load("")
	return m
}

// load reads the hosts from the store and re-applies the search.
func (m *picker) load(keep string) {
	m.all = m.a.st.Hosts()
	sortEntries(m.all)
	m.filter(keep)
}

// filter applies the search and puts the cursor on keep (or the first row).
func (m *picker) filter(keep string) {
	terms := strings.Fields(strings.ToLower(m.search.Value()))
	m.shown = m.shown[:0]
	rows := []table.Row{}
	for _, e := range m.all {
		r := hostRow(e)
		hay := strings.ToLower(strings.Join(r, " "))
		ok := true
		for _, t := range terms {
			ok = ok && strings.Contains(hay, t)
		}
		if ok {
			m.shown = append(m.shown, e)
			rows = append(rows, r)
		}
	}
	m.setColumns(rows)
	m.table.SetRows(rows)
	cur := 0
	for i, e := range m.shown {
		if e.Alias == keep {
			cur = i
		}
	}
	m.table.SetCursor(cur)
}

// setColumns sizes columns to their content, shrinking the widest until the
// table fits the terminal.
func (m *picker) setColumns(rows []table.Row) {
	w := make([]int, len(pickColumns))
	for i, c := range pickColumns {
		w[i] = len(c.title)
		for _, r := range rows {
			w[i] = max(w[i], min(lipgloss.Width(r[i]), c.max))
		}
	}
	avail := m.width - 2*len(w) // cells are padded by one space each side
	for sum(w) > avail {
		widest := 0
		for i := range w {
			if w[i] > w[widest] {
				widest = i
			}
		}
		if w[widest] <= 4 {
			break
		}
		w[widest]--
	}
	cols := make([]table.Column, len(w))
	for i, c := range pickColumns {
		cols[i] = table.Column{Title: c.title, Width: w[i]}
	}
	m.table.SetColumns(cols)
}

func sum(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}

// paneHeight is fixed so the table doesn't jump as the selection changes.
func (m *picker) paneHeight() int {
	if !m.pane {
		return 0
	}
	return min(max(m.height/3, 6), 14)
}

// layout: search bar, table, config pane, status/help line.
func (m *picker) layout() {
	m.search.Width = max(m.width-30, 10)
	m.table.SetHeight(max(m.height-2-m.paneHeight(), 3))
	m.setColumns(m.table.Rows())
}

func (m *picker) selected() *store.Entry {
	if i := m.table.Cursor(); i >= 0 && i < len(m.shown) {
		return m.shown[i]
	}
	return nil
}

func (m *picker) selectedAlias() string {
	if e := m.selected(); e != nil {
		return e.Alias
	}
	return ""
}

// reload re-reads the store after a change and keeps the cursor on alias.
func (m *picker) reload(alias string) {
	st, err := store.Open(m.a.paths)
	if err != nil {
		m.status("reload failed: " + err.Error())
		return
	}
	m.a.st = st
	m.load(alias)
}

// status shows msg, or failing that the last line written to the log.
func (m *picker) status(msg string) {
	if msg == "" {
		lines := strings.Split(strings.TrimSpace(m.log.String()), "\n")
		msg = lines[len(lines)-1]
	}
	m.log.Reset()
	m.msg = msg
}

func (m *picker) Init() tea.Cmd { return textinput.Blink }

func (m *picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case editedMsg:
		if msg.err != nil {
			m.status("editor: " + msg.err.Error())
			return m, nil
		}
		m.a.changed(msg.path, "edit "+filepath.Base(msg.path))
		m.reload(m.selectedAlias())
		m.status("saved " + filepath.Base(msg.path))
		return m, nil
	case addedMsg:
		n := len(m.all)
		m.reload(m.selectedAlias())
		switch {
		case msg.err != nil:
			m.status("add: " + msg.err.Error())
		case len(m.all) > n:
			m.status("host added")
		}
		return m, nil
	case syncedMsg:
		m.reload(m.selectedAlias())
		if msg.err != nil {
			m.status("sync: " + firstLine(msg.err.Error()))
		} else {
			m.status("sync: " + strings.ReplaceAll(strings.TrimSpace(msg.out), "\n", "; "))
		}
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	}
	var cmd tea.Cmd
	m.search, cmd = m.search.Update(msg)
	return m, cmd
}

func (m *picker) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if k == "ctrl+c" {
		return m, tea.Quit
	}
	if m.expanded {
		m.expanded = false // any key closes the full-screen view
		return m, nil
	}
	if m.confirm != nil {
		e := m.confirm
		m.confirm = nil
		if k != "y" && k != "Y" {
			m.status("kept " + e.Alias)
			return m, nil
		}
		if err := m.a.st.Remove(e); err != nil {
			m.status("delete: " + err.Error())
			return m, nil
		}
		m.a.changed(e.Path, "remove "+e.Alias)
		m.reload("")
		m.status("removed " + e.Alias)
		return m, nil
	}
	m.msg = ""
	e := m.selected()
	switch k {
	case "esc":
		if m.search.Value() == "" {
			return m, tea.Quit
		}
		m.search.SetValue("")
		m.filter(m.selectedAlias())
	case "enter":
		if e != nil {
			m.connect = e.Block.Patterns()[0]
			return m, tea.Quit
		}
	case "up", "ctrl+p":
		m.table.MoveUp(1)
	case "down":
		m.table.MoveDown(1)
	case "pgup":
		m.table.MoveUp(m.table.Height())
	case "pgdown":
		m.table.MoveDown(m.table.Height())
	case "home":
		m.table.GotoTop()
	case "end":
		m.table.GotoBottom()
	case "tab":
		m.pane = !m.pane
		m.layout()
	case "ctrl+o":
		m.expanded = e != nil
	case "ctrl+d":
		if e != nil {
			m.confirm = e
			m.msg = fmt.Sprintf("delete %s? y/N", e.Alias)
		}
	case "ctrl+e":
		if e != nil {
			ed := editor()
			path := e.Path
			c := exec.Command(ed[0], append(ed[1:], path)...)
			return m, tea.ExecProcess(c, func(err error) tea.Msg { return editedMsg{path, err} })
		}
	case "ctrl+n":
		exe, err := os.Executable()
		if err != nil {
			m.status(err.Error())
			return m, nil
		}
		return m, tea.ExecProcess(exec.Command(exe, "add"), func(err error) tea.Msg { return addedMsg{err} })
	case "ctrl+s":
		m.msg = "syncing…"
		r := m.a.repo()
		return m, func() tea.Msg {
			out, err := r.Sync("sshync: sync from "+hostname(), "")
			return syncedMsg{out, err}
		}
	default:
		before := m.search.Value()
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		if m.search.Value() != before {
			m.filter("") // best match is the first row
		}
		return m, cmd
	}
	return m, nil
}

const pickHelp = "enter ssh • tab config • ctrl+o expand • ctrl+e edit • ctrl+n add • ctrl+d delete • ctrl+s sync • esc quit"

func (m *picker) detailText(e *store.Entry) string {
	if e == nil {
		return dimStyle.Render("no matching hosts")
	}
	s := dimStyle.Render(fmt.Sprintf("%s (%s)", m.shortPath(e.Path), e.Scope)) + "\n" +
		strings.TrimRight(e.Block.Text("\n"), "\n")
	if n := len(m.a.st.Find(e.Alias, "")); n > 1 {
		s += "\n" + dimStyle.Render(fmt.Sprintf("defined %d times; see `sshync show %s`", n, e.Alias))
	}
	return s
}

func (m *picker) View() string {
	if m.width == 0 {
		return ""
	}
	fit := lipgloss.NewStyle().MaxWidth(m.width)
	if m.expanded {
		box := detailStyle.Width(m.width - 2).MaxHeight(m.height - 1).Render(m.detailText(m.selected()))
		return fit.Render(box + "\n" + dimStyle.Render("any key to go back"))
	}
	top := titleStyle.Render("sshync") + " " + m.search.View()
	count := dimStyle.Render(fmt.Sprintf("%d/%d hosts", len(m.shown), len(m.all)))
	if gap := m.width - lipgloss.Width(top) - lipgloss.Width(count); gap > 0 {
		top += strings.Repeat(" ", gap) + count
	}
	parts := []string{fit.Render(top), fit.Render(m.table.View())}
	if h := m.paneHeight(); h > 0 {
		parts = append(parts, detailStyle.Width(m.width-2).Height(h-2).MaxHeight(h).Render(m.detailText(m.selected())))
	}
	foot := dimStyle.Render(pickHelp)
	if m.msg != "" {
		foot = m.msg
	}
	parts = append(parts, fit.Render(foot))
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// shortPath shows files under the ssh dir as ~/.ssh/...
func (m *picker) shortPath(p string) string {
	if rel, err := filepath.Rel(m.a.paths.SSHDir, p); err == nil && !strings.HasPrefix(rel, "..") {
		return "~/.ssh/" + filepath.ToSlash(rel)
	}
	return p
}

func runPicker(a *app, search string) error {
	out, errOut := a.out, a.errOut
	m := newPicker(a, search)
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
