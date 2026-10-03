package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/ericksonis/sshync/internal/gitsync"
	"github.com/ericksonis/sshync/internal/sshconf"
	"github.com/ericksonis/sshync/internal/store"
	"github.com/spf13/cobra"
)

func initCmd(a *app) *cobra.Command {
	var repoURL, to, prefer string
	var noImport bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up ~/.ssh/sshync, the host repo, and the bootstrap ~/.ssh/config",
		Long: `Creates (or clones with --repo) the synced host repo, backs up an existing
~/.ssh/config to config.bak-<timestamp>, imports its hosts, and replaces it with
a small bootstrap that Includes the sshync files.

On the second machine use --repo <url>: hosts identical to the repo are skipped,
new ones are added, and differing ones are offered for resolution.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := store.DefaultPaths()
			if err != nil {
				return err
			}
			a.paths = p
			scope, err := parseScope(to)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(p.LocalDir, 0o700); err != nil {
				return err
			}
			if err := ensureFile(p.LocalDefaults, "# Machine-local wildcard (Host *) and Match blocks. Not synced.\n"); err != nil {
				return err
			}
			if err := setupRepo(a, p, repoURL); err != nil {
				return err
			}
			if a.st, err = store.Open(p); err != nil {
				return err
			}
			if a.set, err = p.LoadSettings(); err != nil {
				return err
			}

			if p.Managed() {
				a.printf("%s is already managed by sshync\n", p.Config)
				return nil
			}
			preamble := ""
			if data, err := os.ReadFile(p.Config); err == nil {
				backup := p.Config + ".bak-" + time.Now().Format("20060102-150405")
				if err := os.WriteFile(backup, data, 0o600); err != nil {
					return err
				}
				a.printf("backed up %s -> %s\n", p.Config, backup)
				if !noImport {
					res, err := runImport(a, sshconf.Parse(string(data)), scope, prefer)
					if errors.Is(err, errConflicts) {
						a.changed(p.HostsDir, "partial import from "+hostname())
						return fmt.Errorf("%s left unchanged because of the conflicts above; non-conflicting hosts were imported", p.Config)
					}
					if err != nil {
						return err
					}
					preamble = res.Preamble
				} else {
					a.printf("skipped import; run `sshync import %s` later\n", backup)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := store.WriteAtomic(p.Config, []byte(p.Bootstrap(preamble))); err != nil {
				return err
			}
			a.printf("wrote bootstrap %s\n", p.Config)
			a.changed(p.HostsDir, "import from "+hostname())
			return nil
		},
	}
	cmd.Flags().StringVar(&repoURL, "repo", "", "clone an existing host repo from this git URL")
	cmd.Flags().StringVar(&to, "to", "shared", "scope for imported hosts: shared or local")
	cmd.Flags().BoolVar(&noImport, "no-import", false, "back up the existing config but do not import it")
	cmd.Flags().StringVar(&prefer, "prefer", "", "resolve differing hosts without asking: repo or mine")
	return cmd
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func parseScope(s string) (store.Scope, error) {
	switch strings.ToLower(s) {
	case "shared", "":
		return store.Shared, nil
	case "local":
		return store.Local, nil
	}
	return "", fmt.Errorf("scope must be shared or local, not %q", s)
}

func ensureFile(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

func setupRepo(a *app, p store.Paths, url string) error {
	if p.Initialized() {
		if url != "" {
			a.printf("repo already present at %s; ignoring --repo\n", p.Repo)
		}
		return nil
	}
	if url != "" {
		if _, err := os.Stat(p.Repo); err == nil {
			return fmt.Errorf("%s exists but is not an sshync repo (no hosts.d); move it aside first", p.Repo)
		}
		if err := gitsync.Clone(url, p.Repo); err != nil {
			return err
		}
		a.printf("cloned %s -> %s\n", url, p.Repo)
	} else {
		if err := os.MkdirAll(p.Repo, 0o700); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(p.Repo, ".git")); err != nil {
			if err := gitsync.Init(p.Repo); err != nil {
				return err
			}
		}
		a.printf("created repo %s (add a remote with: git -C %q remote add origin <url>)\n", p.Repo, p.Repo)
	}
	// fill in anything missing (also for an empty cloned repo)
	for _, d := range []string{p.HostsDir, p.KeysDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := ensureFile(filepath.Join(d, ".gitkeep"), ""); err != nil {
			return err
		}
	}
	if err := ensureFile(p.Defaults, "# Shared wildcard (Host *) and Match blocks. Included after all hosts.\n"); err != nil {
		return err
	}
	if err := ensureFile(filepath.Join(p.Repo, ".gitattributes"), "* text=auto eol=lf\n"); err != nil {
		return err
	}
	if _, err := os.Stat(p.Settings); errors.Is(err, os.ErrNotExist) {
		if err := p.SaveSettings(store.DefaultSettings()); err != nil {
			return err
		}
	}
	_, err := gitsync.Repo{Dir: p.Repo}.CommitAll("sshync: init")
	return err
}

func importCmd(a *app) *cobra.Command {
	var to, prefer string
	cmd := &cobra.Command{
		Use:   "import [file]",
		Short: "Import Host blocks from a monolithic ssh config (default: newest ~/.ssh/config.bak-*)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			scope, err := parseScope(to)
			if err != nil {
				return err
			}
			path := ""
			if len(args) == 1 {
				path = args[0]
			} else {
				m, _ := filepath.Glob(a.paths.Config + ".bak-*")
				if len(m) == 0 {
					return errors.New("no file given and no ~/.ssh/config.bak-* found")
				}
				path = m[len(m)-1]
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			res, err := runImport(a, sshconf.Parse(string(data)), scope, prefer)
			a.changed(a.paths.HostsDir, "import "+filepath.Base(path)+" from "+hostname())
			if err != nil {
				return err
			}
			if res.Preamble != "" {
				a.printf("note: top-level options were not imported (add them to %s if needed):\n%s\n", a.paths.Config, res.Preamble)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "shared", "scope for new hosts: shared or local")
	cmd.Flags().StringVar(&prefer, "prefer", "", "resolve differing hosts without asking: repo or mine")
	return cmd
}

// errConflicts stops init from replacing ~/.ssh/config while hosts differ.
var errConflicts = errors.New("unresolved conflicts")

// runImport imports f and resolves conflicts: by prompting when interactive,
// else by prefer ("repo" keeps the existing entry, "mine" replaces it).
// Unresolved conflicts return errConflicts.
func runImport(a *app, f *sshconf.File, scope store.Scope, prefer string) (store.ImportResult, error) {
	res, err := a.st.Import(f, scope)
	if err != nil {
		return res, err
	}
	a.printf("imported %d host(s) into %s, %d already identical, %d differ\n",
		len(res.Added), scope, len(res.Skipped), len(res.Conflicts))
	unresolved := 0
	for _, c := range res.Conflicts {
		ok, err := resolveConflict(a, c, prefer)
		if err != nil {
			return res, err
		}
		if !ok {
			unresolved++
		}
	}
	if unresolved > 0 {
		a.printf("\n%d conflict(s) left unresolved. Re-run in a terminal to choose, or pass --prefer repo|mine.\n", unresolved)
		return res, errConflicts
	}
	return res, nil
}

// optionDiff lists options only in a ("-") or only in b ("+").
func optionDiff(a, b *sshconf.Block) []string {
	count := map[string]int{}
	for _, o := range b.Options() {
		count[strings.ToLower(o[0])+" "+o[1]]++
	}
	var out []string
	for _, o := range a.Options() {
		k := strings.ToLower(o[0]) + " " + o[1]
		if count[k] > 0 {
			count[k]--
			continue
		}
		out = append(out, "  - "+o[0]+" "+o[1])
	}
	seen := map[string]int{}
	for _, o := range a.Options() {
		seen[strings.ToLower(o[0])+" "+o[1]]++
	}
	for _, o := range b.Options() {
		k := strings.ToLower(o[0]) + " " + o[1]
		if seen[k] > 0 {
			seen[k]--
			continue
		}
		out = append(out, "  + "+o[0]+" "+o[1])
	}
	return out
}

func resolveConflict(a *app, c store.Conflict, prefer string) (bool, error) {
	a.printf("\n%s differs from the repo (- repo, + this machine):\n%s\n", c.Alias,
		strings.Join(optionDiff(c.Existing.Block, c.Incoming), "\n"))
	choice := prefer
	if choice == "" {
		if !a.interactive {
			return false, nil
		}
		choice = "repo"
		err := huh.NewSelect[string]().
			Title("Resolve "+c.Alias).
			Options(
				huh.NewOption("Keep the repo version", "repo"),
				huh.NewOption("Use this machine's version (updates the repo)", "mine"),
			).Value(&choice).Run()
		if err != nil {
			return false, err
		}
	}
	switch choice {
	case "repo":
		a.printf("kept repo version of %s\n", c.Alias)
	case "mine":
		if err := a.st.Replace(c.Existing, c.Incoming); err != nil {
			return false, err
		}
		a.printf("replaced %s with this machine's version\n", c.Alias)
	default:
		return false, fmt.Errorf("--prefer must be repo or mine, not %q", choice)
	}
	return true, nil
}
