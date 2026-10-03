package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ericksonis/sshync/internal/gitsync"
)

const sample = `# comment
Host alpha
    HostName alpha.example.com
	User eis
    IdentityFile ~/.ssh/k.pub
    ForwardAgent yes

Host beta
	HostName beta.example.com
	User bob
	LocalForward 3400 one.example.com:3389

Host *
    ServerAliveInterval 30
`

const pub = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleExampleExampleExampleExampleExample test\n"

func setupEnv(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "gitconfig")
	os.WriteFile(empty, nil, 0o644)
	for k, v := range map[string]string{
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": empty,
	} {
		t.Setenv(k, v)
	}
}

// machine creates a fake ~/.ssh with the given config and returns its path.
func machine(t *testing.T, config string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "k.pub"), []byte(pub), 0o644)
	if config != "" {
		os.WriteFile(filepath.Join(dir, "config"), []byte(config), 0o600)
	}
	return dir
}

func run(t *testing.T, sshDir string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("SSHYNC_SSH_DIR", sshDir)
	var out bytes.Buffer
	root := newRoot(&app{out: &out, errOut: &out, in: strings.NewReader("")})
	root.SetArgs(args)
	root.SetErr(&out)
	err := root.Execute()
	return out.String(), err
}

func must(t *testing.T, sshDir string, args ...string) string {
	t.Helper()
	out, err := run(t, sshDir, args...)
	if err != nil {
		t.Fatalf("sshync %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sshG(t *testing.T, config, alias string) string {
	t.Helper()
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh not installed")
	}
	out, err := exec.Command(ssh, "-F", config, "-G", alias).Output()
	if err != nil {
		t.Fatalf("ssh -G %s: %v", alias, err)
	}
	return string(out)
}

func TestInitImportPreservesEffectiveConfig(t *testing.T) {
	setupEnv(t)
	d := machine(t, sample)
	backupless := filepath.Join(t.TempDir(), "orig")
	os.WriteFile(backupless, []byte(sample), 0o600)

	must(t, d, "init")
	cfg := filepath.Join(d, "config")
	if !strings.HasPrefix(readFile(t, cfg), "# Managed by sshync") {
		t.Fatal("bootstrap not written")
	}
	if got := readFile(t, filepath.Join(d, "sshync", "repo", "hosts.d", "beta.conf")); got != "Host beta\n\tHostName beta.example.com\n\tUser bob\n\tLocalForward 3400 one.example.com:3389\n" {
		t.Errorf("beta.conf = %q", got)
	}
	for _, alias := range []string{"alpha", "beta", "other"} {
		if a, b := sshG(t, backupless, alias), sshG(t, cfg, alias); a != b {
			t.Errorf("%s: ssh -G differs after import", alias)
		}
	}
	// re-running init is a no-op
	if out := must(t, d, "init"); !strings.Contains(out, "already managed") {
		t.Errorf("second init: %s", out)
	}
}

func TestEditCommands(t *testing.T) {
	setupEnv(t)
	d := machine(t, "")
	must(t, d, "init")
	must(t, d, "add", "web", "-H", "web.example.com", "-u", "deploy", "-k", "k", "-A", "-y")
	f := filepath.Join(d, "sshync", "repo", "hosts.d", "web.conf")
	if !fileExists(filepath.Join(d, "sshync", "repo", "keys", "k.pub")) {
		t.Error("public key not copied into repo")
	}
	must(t, d, "set", "web", "port", "2222")
	must(t, d, "fwd", "add", "web", "-L", "8080:localhost:80", "-D", "1080")
	must(t, d, "toggle", "web", "IdentitiesOnly")
	must(t, d, "toggle", "web", "ForwardAgent")
	want := "Host web\n" +
		"    HostName web.example.com\n" +
		"    User deploy\n" +
		"    IdentityFile ~/.ssh/sshync/repo/keys/k.pub\n" +
		"    IdentitiesOnly no\n" +
		"    ForwardAgent no\n" +
		"    Port 2222\n" +
		"    LocalForward 8080 localhost:80\n" +
		"    DynamicForward 1080\n"
	if got := readFile(t, f); got != want {
		t.Errorf("web.conf\n%s\nwant\n%s", got, want)
	}
	must(t, d, "fwd", "rm", "web", "-L", "8080:localhost:80")
	must(t, d, "unset", "web", "DynamicForward")
	if strings.Contains(readFile(t, f), "Forward 8080") || strings.Contains(readFile(t, f), "Dynamic") {
		t.Error("forwards not removed")
	}
	if _, err := run(t, d, "set", "web", "Bogus", "x"); err == nil {
		t.Error("unknown keyword accepted")
	}
	if _, err := run(t, d, "add", "web", "-y"); err == nil {
		t.Error("duplicate add accepted")
	}
	must(t, d, "rename", "web", "web2")
	must(t, d, "mv", "web2", "--to", "local")
	if !fileExists(filepath.Join(d, "sshync", "local.d", "web2.conf")) || fileExists(f) {
		t.Error("rename/mv files")
	}
	if out := must(t, d, "list", "web"); !strings.Contains(out, "web2") || !strings.Contains(out, "local") {
		t.Errorf("list: %s", out)
	}
	if out := must(t, d, "show", "web2"); !strings.Contains(out, "Port 2222") {
		t.Errorf("show: %s", out)
	}
	must(t, d, "rm", "web2", "-y")
	if out := must(t, d, "list", "--json"); strings.TrimSpace(out) != "[]" {
		t.Errorf("list after rm: %s", out)
	}
	log, _ := gitsync.Repo{Dir: filepath.Join(d, "sshync", "repo")}.Git("log", "--oneline")
	if n := len(strings.Split(log, "\n")); n < 8 {
		t.Errorf("expected a commit per shared edit, got %d:\n%s", n, log)
	}
}

func TestKeysRejectPrivate(t *testing.T) {
	setupEnv(t)
	d := machine(t, "")
	must(t, d, "init")
	priv := filepath.Join(t.TempDir(), "id")
	os.WriteFile(priv, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nxx\n-----END OPENSSH PRIVATE KEY-----\n"), 0o600)
	if _, err := run(t, d, "keys", "add", "bad", priv); err == nil {
		t.Fatal("private key accepted")
	}
	must(t, d, "keys", "add", "good", filepath.Join(d, "k.pub"))
	if out := must(t, d, "keys", "list"); !strings.Contains(out, "good.pub") {
		t.Errorf("keys list: %s", out)
	}
}

func TestTwoMachineSync(t *testing.T) {
	setupEnv(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	if err := exec.Command("git", "init", "-q", "--bare", "-b", "main", remote).Run(); err != nil {
		t.Fatal(err)
	}
	a := machine(t, sample)
	must(t, a, "init")
	if _, err := (gitsync.Repo{Dir: filepath.Join(a, "sshync", "repo")}).Git("remote", "add", "origin", remote); err != nil {
		t.Fatal(err)
	}
	must(t, a, "sync")

	// machine B has drifted: beta differs, gamma is new
	drift := strings.Replace(sample, "User bob", "User robert", 1) + "\nHost gamma\n    HostName gamma.lan\n"
	b := machine(t, drift)
	out, err := run(t, b, "init", "--repo", remote)
	if err == nil || !strings.Contains(out, "+ User robert") {
		t.Fatalf("expected unresolved conflict, got err=%v\n%s", err, out)
	}
	if strings.HasPrefix(readFile(t, filepath.Join(b, "config")), "# Managed") {
		t.Fatal("bootstrap written despite conflicts")
	}
	must(t, b, "init", "--prefer", "mine")
	must(t, b, "sync")
	must(t, a, "sync")
	if out := must(t, a, "show", "beta"); !strings.Contains(out, "User robert") {
		t.Errorf("A did not receive B's beta: %s", out)
	}
	if len(must(t, a, "list", "gamma")) < 40 {
		t.Error("A did not receive gamma")
	}

	// both change the same host: sync aborts cleanly, then --prefer resolves
	must(t, a, "set", "alpha", "Port", "2200")
	must(t, b, "set", "alpha", "Port", "2300")
	must(t, a, "sync")
	_, err = run(t, b, "sync")
	var ce *gitsync.ConflictError
	if !errors.As(err, &ce) || len(ce.Files) != 1 {
		t.Fatalf("expected ConflictError, got %v", err)
	}
	alpha := filepath.Join(b, "sshync", "repo", "hosts.d", "alpha.conf")
	if strings.Contains(readFile(t, alpha), "<<<<") {
		t.Fatal("conflict markers left in working tree")
	}
	must(t, b, "sync", "--prefer", "mine")
	must(t, a, "sync")
	if out := must(t, a, "show", "alpha"); !strings.Contains(out, "Port 2300") {
		t.Errorf("prefer mine not propagated: %s", out)
	}
}
