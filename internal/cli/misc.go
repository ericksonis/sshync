package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ericksonis/sshync/internal/gitsync"
	"github.com/ericksonis/sshync/internal/sshconf"
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
	})
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
	return &cobra.Command{
		Use: "doctor", Short: "Check the setup for common problems", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			problems := 0
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
				if len(ids) > 0 && !strings.EqualFold(e.Block.First("IdentitiesOnly"), "yes") {
					warn("%s: has IdentityFile but not IdentitiesOnly yes (the agent may offer every key) -> sshync toggle %s IdentitiesOnly", e.Alias, e.Alias)
				}
				for _, l := range e.Block.Body {
					if l.Kind == sshconf.KV && !sshconf.KnownKey(l.Key) {
						warn("%s: unknown keyword %s", e.Alias, l.Key)
					}
				}
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
