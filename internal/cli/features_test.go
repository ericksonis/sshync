package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ericksonis/sshync/internal/gitsync"
)

func TestCompletion(t *testing.T) {
	setupEnv(t)
	d := machine(t, sample)

	// before init: no suggestions, no error
	if out := must(t, d, "__complete", "show", ""); strings.Contains(out, "alpha") {
		t.Errorf("uninitialised completion: %s", out)
	}
	must(t, d, "init")
	cases := []struct {
		args       []string
		want, nope []string
	}{
		{[]string{"show", ""}, []string{"alpha\talpha.example.com", "beta\tbeta.example.com"}, []string{"*"}},
		{[]string{"show", "al"}, []string{"alpha"}, []string{"beta"}},
		{[]string{"set", "alpha", "Ident"}, []string{"IdentitiesOnly", "IdentityFile"}, []string{"Host\n"}},
		{[]string{"set", "alpha", "IdentityFile", ""}, []string{"~/.ssh/k.pub"}, nil},
		{[]string{"set", "alpha", "ForwardAgent", ""}, []string{"yes", "no"}, nil},
		{[]string{"unset", "beta", ""}, []string{"HostName", "User", "LocalForward"}, []string{"Port"}},
		{[]string{"toggle", "alpha", "F"}, []string{"ForwardAgent"}, []string{"Port"}},
		{[]string{"fwd", "rm", "beta", "-L", ""}, []string{"3400:one.example.com:3389"}, nil},
		{[]string{"sync", "--prefer", ""}, []string{"mine", "remote"}, nil},
		{[]string{"mv", "alpha", "--to", ""}, []string{"local", "shared"}, nil},
		{[]string{"add", "x", "--key", ""}, []string{"~/.ssh/k.pub"}, nil},
	}
	for _, c := range cases {
		out := must(t, d, append([]string{"__complete"}, c.args...)...)
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("complete %q: missing %q in\n%s", c.args, w, out)
			}
		}
		for _, n := range c.nope {
			if strings.Contains(out, n) {
				t.Errorf("complete %q: unexpected %q in\n%s", c.args, n, out)
			}
		}
	}
	if out := must(t, d, "completion", "powershell"); !strings.Contains(out, "Register-ArgumentCompleter") {
		t.Error("powershell completion script missing")
	}
}

func TestKeysAgent(t *testing.T) {
	setupEnv(t)
	d := machine(t, "")
	must(t, d, "init")
	must(t, d, "keys", "add", "existing", filepath.Join(d, "k.pub"))

	saved := agentKeys
	t.Cleanup(func() { agentKeys = saved })
	agentKeys = func() (string, error) {
		return strings.Join([]string{
			strings.TrimSpace(pub), // already in the repo under another name
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIWorkWorkWorkWorkWorkWorkWorkWorkWorkWork Work Laptop",
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOtherOtherOtherOtherOtherOtherOtherOther Work Laptop",
			"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQNoCommentNoComment12345678",
			"",
		}, "\n"), nil
	}
	out, err := run(t, d, "keys", "agent")
	if err == nil || !strings.Contains(out, "work-laptop.pub") {
		t.Fatalf("non-interactive without --all should list and fail: %v\n%s", err, out)
	}
	must(t, d, "keys", "agent", "--all")
	keys := filepath.Join(d, "sshync", "repo", "keys")
	for name, want := range map[string]string{
		"work-laptop.pub":   "WorkWork",
		"work-laptop-2.pub": "OtherOther",
		"rsa-12345678.pub":  "NoComment",
		"existing.pub":      "Example",
	} {
		if got := readFile(t, filepath.Join(keys, name)); !strings.Contains(got, want) {
			t.Errorf("%s = %q", name, got)
		}
	}
	if fileExists(filepath.Join(keys, "test.pub")) {
		t.Error("key already in the repo was added again")
	}
	if out := must(t, d, "keys", "agent", "--all"); !strings.Contains(out, "no new keys") {
		t.Errorf("second run: %s", out)
	}
}

func TestDoctorFix(t *testing.T) {
	setupEnv(t)
	d := machine(t, sample+"\nHost explicit\n    IdentityFile ~/.ssh/k.pub\n    IdentitiesOnly no\n")
	must(t, d, "init")
	if out := must(t, d, "doctor"); !strings.Contains(out, "alpha: has IdentityFile but not IdentitiesOnly") {
		t.Errorf("doctor: %s", out)
	}
	out := must(t, d, "doctor", "--fix")
	if !strings.Contains(out, "added IdentitiesOnly yes to 1 host(s)") {
		t.Errorf("doctor --fix: %s", out)
	}
	hosts := filepath.Join(d, "sshync", "repo", "hosts.d")
	if !strings.Contains(readFile(t, filepath.Join(hosts, "alpha.conf")), "IdentitiesOnly yes") {
		t.Error("alpha not fixed")
	}
	if !strings.Contains(readFile(t, filepath.Join(hosts, "explicit.conf")), "IdentitiesOnly no") {
		t.Error("explicit IdentitiesOnly no was overridden")
	}
	if st, _ := (gitsync.Repo{Dir: filepath.Join(d, "sshync", "repo")}).Git("status", "--porcelain"); st != "" {
		t.Errorf("fix not committed: %s", st)
	}
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+d":
		return tea.KeyMsg{Type: tea.KeyCtrlD}
	case "ctrl+o":
		return tea.KeyMsg{Type: tea.KeyCtrlO}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func newTestPicker(t *testing.T, search string, w, h int) (*picker, string) {
	t.Helper()
	setupEnv(t)
	d := machine(t, sample)
	must(t, d, "init")
	t.Setenv("SSHYNC_SSH_DIR", d)
	a := &app{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	if err := a.open(); err != nil {
		t.Fatal(err)
	}
	m := newPicker(a, search)
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m, d
}

func TestPicker(t *testing.T) {
	m, d := newTestPicker(t, "", 120, 30)
	if n := len(m.shown); n != 2 {
		t.Fatalf("rows = %d", n)
	}
	v := m.View()
	for _, want := range []string{"Alias", "HostName", "alpha.example.com", "bob", "2/2 hosts",
		"~/.ssh/sshync/repo/hosts.d/alpha.conf", "ctrl+d delete"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if w, h := lipgloss.Width(v), lipgloss.Height(v); w > 120 || h > 30 {
		t.Errorf("view is %dx%d, terminal 120x30:\n%s", w, h, v)
	}
	alpha := filepath.Join(d, "sshync", "repo", "hosts.d", "alpha.conf")
	before := readFile(t, alpha)

	// plain letters only search: i and F must not toggle anything
	m.Update(keyMsg("i"))
	m.Update(keyMsg("F"))
	if readFile(t, alpha) != before || m.search.Value() != "iF" {
		t.Errorf("letters should go to the search, got %q", m.search.Value())
	}
	m.Update(keyMsg("esc"))
	if m.search.Value() != "" || len(m.shown) != 2 {
		t.Fatal("esc should clear the search")
	}

	m.Update(keyMsg("tab"))
	if strings.Contains(m.View(), "alpha.conf") {
		t.Error("tab should hide the config pane")
	}
	m.Update(keyMsg("ctrl+o"))
	if !m.expanded || !strings.Contains(m.View(), "HostName alpha.example.com") {
		t.Error("ctrl+o should show the config full-screen")
	}
	m.Update(keyMsg("q"))
	if m.expanded || m.search.Value() != "" {
		t.Error("a key should close the full-screen view without typing")
	}

	m.Update(keyMsg("ctrl+d"))
	m.Update(keyMsg("n"))
	if !fileExists(alpha) {
		t.Fatal("delete went ahead without y")
	}
	m.Update(keyMsg("ctrl+d"))
	m.Update(keyMsg("y"))
	if fileExists(alpha) || len(m.shown) != 1 {
		t.Fatal("ctrl+d y did not delete alpha")
	}
	log, _ := gitsync.Repo{Dir: filepath.Join(d, "sshync", "repo")}.Git("log", "--format=%s", "-1")
	if !strings.Contains(log, "remove alpha") {
		t.Errorf("delete not committed: %s", log)
	}

	_, cmd := m.Update(keyMsg("enter"))
	if m.connect != "beta" || cmd == nil {
		t.Fatalf("enter: connect=%q", m.connect)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("enter should quit the picker")
	}
}

func TestPickerSearch(t *testing.T) {
	m, _ := newTestPicker(t, "bet", 80, 24)
	if len(m.shown) != 1 || m.shown[0].Alias != "beta" {
		t.Errorf("search arg: %v", m.shown)
	}
	m.Update(keyMsg("esc"))
	for _, r := range "eis alpha" {
		m.Update(keyMsg(string(r)))
	}
	if len(m.shown) != 1 || m.shown[0].Alias != "alpha" {
		t.Errorf("all terms must match: %v", m.shown)
	}
	m.Update(keyMsg("esc"))
	m.Update(keyMsg("down"))
	if m.selectedAlias() != "beta" {
		t.Errorf("down: %q", m.selectedAlias())
	}
	if w := lipgloss.Width(m.View()); w > 80 {
		t.Errorf("view width %d at 80 columns", w)
	}
}

func TestKeySlug(t *testing.T) {
	for in, want := range map[string]string{
		"SSH Key - GUILE":                "ssh-key-guile",
		"SSH Key - oob.aero.erickson.is": "ssh-key-oob.aero.erickson.is",
		"Grow BI Jump Host SSH Key":      "grow-bi-jump-host-ssh-key",
		"  Observium - Telmanager  ":     "observium-telmanager",
		"me@laptop.pub":                  "me-laptop",
		"--- ":                           "",
	} {
		if got := keySlug(in); got != want {
			t.Errorf("keySlug(%q) = %q, want %q", in, got, want)
		}
	}
}
