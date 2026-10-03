package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/ericksonis/sshync/internal/gitsync"
	"github.com/ericksonis/sshync/internal/sshconf"
	"github.com/ericksonis/sshync/internal/store"
	"github.com/spf13/cobra"
)

// copyPub copies a public key, refusing anything that looks like a private key.
func copyPub(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writePub(data, dst)
}

func writePub(data []byte, dst string) error {
	s := strings.TrimSpace(string(data))
	if strings.Contains(s, "PRIVATE KEY") {
		return errors.New("refusing to store a private key; give the .pub file")
	}
	f := strings.Fields(s)
	if len(f) < 2 || !(strings.HasPrefix(f[0], "ssh-") || strings.HasPrefix(f[0], "ecdsa-") || strings.HasPrefix(f[0], "sk-")) {
		return errors.New("not an OpenSSH public key (expected e.g. \"ssh-ed25519 AAAA... comment\")")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(s+"\n"), 0o644)
}

func keysCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "keys", Short: "Manage public keys in the synced repo"}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List repo keys and which hosts use them", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			used := map[string][]string{}
			for _, e := range a.st.Entries {
				for _, f := range e.Block.Get("IdentityFile") {
					b := shortKey(f)
					used[b] = append(used[b], e.Alias)
				}
			}
			for _, k := range a.st.Keys() {
				a.printf("%-40s %d host(s)\n", k, len(used[k]))
			}
			return nil
		},
	}, &cobra.Command{
		Use:   "add <name> [file|-]",
		Short: "Add a public key to the repo (from a file, stdin, or ~/.ssh/<name>)",
		Long:  "Copy the public key from Bitwarden (SSH key item > Public key) and pipe it in, e.g.\n  Get-Clipboard | sshync keys add work_laptop -",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !strings.HasSuffix(name, ".pub") {
				name += ".pub"
			}
			if strings.ContainsAny(name, "/\\") {
				return errors.New("name must be a plain file name")
			}
			dst := filepath.Join(a.paths.KeysDir, name)
			if fileExists(dst) {
				return fmt.Errorf("%s already exists", dst)
			}
			var data []byte
			var err error
			switch {
			case len(args) == 2 && args[1] == "-":
				data, err = io.ReadAll(a.in)
			case len(args) == 2:
				data, err = os.ReadFile(args[1])
			default:
				data, err = os.ReadFile(filepath.Join(a.paths.SSHDir, name))
			}
			if err != nil {
				return err
			}
			if err := writePub(data, dst); err != nil {
				return err
			}
			a.printf("added %s (use with: sshync set <alias> IdentityFile %s)\n", dst, a.paths.KeyRef(name))
			a.changed(dst, "add key "+name)
			return nil
		},
	}, keysAgentCmd(a))
	return cmd
}

// agentKeys returns `ssh-add -L` output; a variable so tests can fake the agent.
var agentKeys = func() (string, error) {
	out, err := exec.Command("ssh-add", "-L").CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return "", nil // "The agent has no identities."
	}
	if err != nil {
		return "", fmt.Errorf("ssh-add -L: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

type agentKey struct{ name, line string }

// newAgentKeys lists agent keys whose key material isn't already in the repo,
// named after the key comment (Bitwarden uses the vault item name).
func newAgentKeys(a *app, listing string) []agentKey {
	have := map[string]bool{}
	taken := map[string]bool{}
	for _, k := range a.st.Keys() {
		taken[strings.ToLower(k)] = true
		if data, err := os.ReadFile(filepath.Join(a.paths.KeysDir, k)); err == nil {
			if f := strings.Fields(string(data)); len(f) >= 2 {
				have[f[1]] = true
			}
		}
	}
	var out []agentKey
	for _, line := range strings.Split(listing, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || have[f[1]] {
			continue
		}
		have[f[1]] = true
		base := strings.TrimSuffix(strings.TrimSuffix(store.FileName(strings.Join(f[2:], " ")), ".conf"), ".pub")
		if base == "_" {
			base = strings.TrimPrefix(f[0], "ssh-") + "-" + f[1][max(0, len(f[1])-8):]
		}
		name := base + ".pub"
		for i := 2; taken[strings.ToLower(name)]; i++ {
			name = fmt.Sprintf("%s-%d.pub", base, i)
		}
		taken[strings.ToLower(name)] = true
		out = append(out, agentKey{name, strings.TrimSpace(line)})
	}
	return out
}

func keysAgentCmd(a *app) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:     "agent",
		Aliases: []string{"pull"},
		Short:   "Add public keys from the running ssh-agent (e.g. Bitwarden) to the repo",
		Long: `Reads public keys from the ssh-agent (ssh-add -L) and adds the ones not yet in
the repo, named after each key's comment. Only public keys are ever read; the
agent does not hand out private keys.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			listing, err := agentKeys()
			if err != nil {
				return err
			}
			cands := newAgentKeys(a, listing)
			if len(cands) == 0 {
				a.printf("no new keys in the agent\n")
				return nil
			}
			chosen := cands
			if !all {
				if !a.interactive {
					for _, c := range cands {
						a.printf("%s\n", c.name)
					}
					return errors.New("pass --all to add these, or run in a terminal to pick")
				}
				opts := make([]huh.Option[int], len(cands))
				for i, c := range cands {
					opts[i] = huh.NewOption(c.name, i)
				}
				var picked []int
				if err := huh.NewMultiSelect[int]().Title("Add which keys to the repo?").
					Options(opts...).Value(&picked).Height(min(len(cands)+2, 20)).Run(); err != nil {
					return err
				}
				chosen = nil
				for _, i := range picked {
					chosen = append(chosen, cands[i])
				}
			}
			for _, c := range chosen {
				if err := writePub([]byte(c.line), filepath.Join(a.paths.KeysDir, c.name)); err != nil {
					return fmt.Errorf("%s: %w", c.name, err)
				}
				a.printf("added %s\n", a.paths.KeyRef(c.name))
			}
			if len(chosen) > 0 {
				a.changed(a.paths.KeysDir, fmt.Sprintf("add %d key(s) from agent", len(chosen)))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "add every new key without asking")
	return cmd
}

func syncCmd(a *app) *cobra.Command {
	var prefer string
	cmd := &cobra.Command{
		Use: "sync", Short: "Commit, pull --rebase, and push the host repo", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p := gitsync.Prefer(prefer)
			if p != gitsync.PreferNone && p != gitsync.PreferMine && p != gitsync.PreferRemote {
				return fmt.Errorf("--prefer must be mine or remote, not %q", prefer)
			}
			out, err := a.repo().Sync("sshync: sync from "+hostname(), p)
			if out != "" {
				a.printf("%s\n", out)
			}
			return err
		},
	}
	cmd.Flags().StringVar(&prefer, "prefer", "", "when a host changed on both machines: mine or remote")
	return cmd
}

func doctorCmd(a *app) *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use: "doctor", Short: "Check the setup for common problems (--fix adds missing IdentitiesOnly yes)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			problems, fixed := 0, 0
			warn := func(format string, args ...any) {
				problems++
				a.printf("! "+format+"\n", args...)
			}
			if !a.paths.Managed() {
				warn("%s is not the sshync bootstrap (run `sshync init`)", a.paths.Config)
			}
			if home := os.Getenv("HOME"); home != "" {
				if up, _ := os.UserHomeDir(); !samePath(home, up) {
					a.printf("i HOME=%s differs from your profile dir; sshync's Include paths are absolute so this is fine,\n  but avoid \"~\" in Include lines you add by hand\n", home)
				}
			}
			seen := map[string]string{}
			for _, e := range a.st.Hosts() {
				for _, p := range e.Block.Patterns() {
					if prev, ok := seen[p]; ok && e.Scope == "shared" {
						a.printf("i %s is defined in %s and %s (the first one wins per option)\n", p, prev, e.Path)
					}
					seen[p] = e.Path
				}
				ids := e.Block.Get("IdentityFile")
				for _, f := range ids {
					f = strings.Trim(f, "\"")
					if strings.ContainsAny(f, "%$") {
						continue
					}
					if !fileExists(expandHome(f)) {
						warn("%s: IdentityFile %s does not exist", e.Alias, f)
					} else if e.Scope == "shared" && !strings.HasSuffix(f, ".pub") {
						a.printf("i %s: IdentityFile %s is a private key file on this machine; it must exist on every machine (or point at a .pub held by the agent)\n", e.Alias, f)
					}
				}
				switch idOnly := strings.ToLower(e.Block.First("IdentitiesOnly")); {
				case len(ids) == 0 || idOnly == "yes":
				case idOnly == "no":
					a.printf("i %s: IdentitiesOnly no is set explicitly; the agent may offer every key\n", e.Alias)
				case fix:
					e.Block.Set("IdentitiesOnly", "yes")
					if err := a.st.Save(e); err != nil {
						return err
					}
					fixed++
				default:
					warn("%s: has IdentityFile but not IdentitiesOnly yes (the agent may offer every key); fix all with `sshync doctor --fix`", e.Alias)
				}
				for _, l := range e.Block.Body {
					if l.Kind == sshconf.KV && !sshconf.KnownKey(l.Key) {
						warn("%s: unknown keyword %s", e.Alias, l.Key)
					}
				}
			}
			if fixed > 0 {
				a.printf("added IdentitiesOnly yes to %d host(s)\n", fixed)
				a.changed(a.paths.HostsDir, fmt.Sprintf("doctor: IdentitiesOnly yes on %d host(s)", fixed))
			}
			if st, err := a.repo().Status(); err != nil {
				warn("git: %v", err)
			} else {
				lines := strings.Split(st, "\n")
				if len(lines) > 1 {
					warn("repo has uncommitted changes (run `sshync sync`)")
				}
				if strings.Contains(lines[0], "ahead") || strings.Contains(lines[0], "behind") {
					warn("repo is out of sync with its remote: %s (run `sshync sync`)", strings.TrimPrefix(lines[0], "## "))
				}
				if !a.repo().HasRemote() {
					warn("repo has no remote; add one: git -C %q remote add origin <url>", a.paths.Repo)
				}
			}
			if out, err := exec.Command("ssh", "-F", a.paths.Config, "-G", "sshync-doctor-probe").CombinedOutput(); err != nil {
				warn("`ssh -G` fails with the current config: %s", strings.TrimSpace(string(out)))
			}
			if problems == 0 {
				a.printf("ok\n")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "add IdentitiesOnly yes to hosts that have an IdentityFile but no IdentitiesOnly line")
	return cmd
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		if h, err := os.UserHomeDir(); err == nil {
			// SSHYNC_SSH_DIR (tests) relocates ~/.ssh
			if d := os.Getenv("SSHYNC_SSH_DIR"); d != "" && strings.HasPrefix(p[2:], ".ssh") {
				return filepath.Join(d, p[len("~/.ssh"):])
			}
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

func samePath(a, b string) bool {
	a, b = filepath.Clean(filepath.FromSlash(a)), filepath.Clean(filepath.FromSlash(b))
	return strings.EqualFold(a, b)
}
