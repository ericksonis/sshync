package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/charmbracelet/huh"
	"github.com/ericksonis/sshync/internal/sshconf"
	"github.com/ericksonis/sshync/internal/store"
	"github.com/spf13/cobra"
)

func addCmd(a *app) *cobra.Command {
	var hostName, user, key, port string
	var opts []string
	var yes, local, fwdAgent, noIdOnly bool
	cmd := &cobra.Command{
		Use:   "add [alias]",
		Short: "Add a host (prompts for anything not given as a flag)",
		Example: `  sshync add web1 -H web1.example.com -u deploy -k id_ed25519_work.pub
  sshync add db -H 10.0.0.5 -p 2222 -o "LocalForward=5432 localhost:5432" -y
  sshync add                      # fully interactive`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			alias := ""
			if len(args) == 1 {
				alias = args[0]
			}
			scope := store.Shared
			if local {
				scope = store.Local
			}
			if !cmd.Flags().Changed("user") {
				user = a.set.Defaults.User
			}
			if !cmd.Flags().Changed("key") {
				key = a.set.Defaults.Identity
			}
			if !cmd.Flags().Changed("forward-agent") {
				fwdAgent = a.set.Defaults.ForwardAgent
			}
			if a.interactive && !yes {
				if err := addForm(a, &alias, &hostName, &user, &key, &port, &scope, cmd); err != nil {
					return err
				}
			}
			if alias == "" {
				return errors.New("alias required")
			}
			if strings.ContainsAny(alias, "*?! \t") {
				return errors.New("alias must be a single literal name (use `sshync edit` for wildcard blocks)")
			}
			if hostName == "" {
				hostName = alias
			}
			if es := a.st.Find(alias, ""); len(es) > 0 {
				return fmt.Errorf("host %q already exists in %s", alias, es[0].Path)
			}
			b := sshconf.NewHostBlock(alias)
			b.Add("HostName", hostName)
			if user != "" {
				b.Add("User", user)
			}
			if port != "" && port != "22" {
				b.Add("Port", port)
			}
			if key != "" {
				ref, err := resolveKey(a, key, scope)
				if err != nil {
					return err
				}
				b.Add("IdentityFile", sshconf.Quote(ref))
				if a.set.Defaults.IdentitiesOnly && !noIdOnly {
					b.Add("IdentitiesOnly", "yes")
				}
			}
			if fwdAgent {
				b.Add("ForwardAgent", "yes")
			}
			for _, o := range opts {
				k, v, ok := strings.Cut(o, "=")
				if !ok {
					k, v, ok = strings.Cut(o, " ")
				}
				if !ok || !sshconf.KnownKey(k) {
					return fmt.Errorf("bad -o %q: want Key=Value with a known ssh_config keyword", o)
				}
				if sshconf.Repeatable(k) {
					b.Add(k, v)
				} else {
					b.Set(k, v)
				}
			}
			e, err := a.st.Put(b, scope)
			if err != nil {
				return err
			}
			a.printf("added %s (%s) -> %s\n%s", alias, scope, e.Path, b.Text("\n"))
			a.changed(e.Path, "add "+alias)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&hostName, "hostname", "H", "", "HostName (default: alias)")
	f.StringVarP(&user, "user", "u", "", "User (default from sshync.toml)")
	f.StringVarP(&key, "key", "k", "", "identity: name in keys/, ~/.ssh/*.pub name, or a path; \"\" for none")
	f.StringVarP(&port, "port", "p", "", "Port")
	f.StringArrayVarP(&opts, "option", "o", nil, "extra option Key=Value (repeatable)")
	f.BoolVarP(&yes, "yes", "y", false, "don't prompt; use flags and defaults")
	f.BoolVar(&local, "local", false, "add to this machine only (not synced)")
	f.BoolVarP(&fwdAgent, "forward-agent", "A", false, "ForwardAgent yes")
	f.BoolVar(&noIdOnly, "no-identities-only", false, "don't add IdentitiesOnly yes")
	return cmd
}

func addForm(a *app, alias, hostName, user, key, port *string, scope *store.Scope, cmd *cobra.Command) error {
	var fields []huh.Field
	if *alias == "" {
		fields = append(fields, huh.NewInput().Title("Alias").Value(alias).Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("required")
			}
			if len(a.st.Find(s, "")) > 0 {
				return errors.New("already exists")
			}
			return nil
		}))
	}
	if !cmd.Flags().Changed("hostname") {
		fields = append(fields, huh.NewInput().Title("HostName").Description("blank = same as alias").Value(hostName))
	}
	if !cmd.Flags().Changed("user") {
		fields = append(fields, huh.NewInput().Title("User").Value(user))
	}
	if !cmd.Flags().Changed("key") {
		opts := []huh.Option[string]{huh.NewOption("(none)", "")}
		for _, k := range keyChoices(a) {
			opts = append(opts, huh.NewOption(k, k))
		}
		fields = append(fields, huh.NewSelect[string]().Title("Identity").Options(opts...).Value(key).Height(12))
	}
	if !cmd.Flags().Changed("port") {
		fields = append(fields, huh.NewInput().Title("Port").Placeholder("22").Value(port))
	}
	if !cmd.Flags().Changed("local") {
		fields = append(fields, huh.NewSelect[store.Scope]().Title("Scope").Options(
			huh.NewOption("shared (synced)", store.Shared), huh.NewOption("local (this machine only)", store.Local),
		).Value(scope))
	}
	if len(fields) == 0 {
		return nil
	}
	return huh.NewForm(huh.NewGroup(fields...)).Run()
}

// keyChoices lists repo keys, then ~/.ssh/*.pub not already in the repo.
func keyChoices(a *app) []string {
	repo := a.st.Keys()
	seen := map[string]bool{}
	for _, k := range repo {
		seen[k] = true
	}
	out := append([]string{}, repo...)
	m, _ := filepath.Glob(filepath.Join(a.paths.SSHDir, "*.pub"))
	for _, p := range m {
		if b := filepath.Base(p); !seen[b] {
			out = append(out, "~/.ssh/"+b)
		}
	}
	return out
}

// resolveKey turns a key name into an IdentityFile value. Public keys from
// ~/.ssh used by shared hosts are copied into the repo so the other machine
// has them too. Private keys are never copied.
func resolveKey(a *app, key string, scope store.Scope) (string, error) {
	name := strings.TrimPrefix(strings.TrimPrefix(key, "~/.ssh/"), "~\\.ssh\\")
	for _, n := range []string{name, name + ".pub"} {
		if fileExists(filepath.Join(a.paths.KeysDir, n)) && !strings.ContainsAny(n, "/\\") {
			return a.paths.KeyRef(n), nil
		}
	}
	if !strings.ContainsAny(name, "/\\") {
		for _, n := range []string{name, name + ".pub"} {
			src := filepath.Join(a.paths.SSHDir, n)
			if !fileExists(src) {
				continue
			}
			if scope == store.Shared && strings.HasSuffix(n, ".pub") {
				if err := copyPub(src, filepath.Join(a.paths.KeysDir, n)); err != nil {
					return "", err
				}
				a.printf("copied %s into %s\n", n, a.paths.KeysDir)
				return a.paths.KeyRef(n), nil
			}
			if scope == store.Shared {
				fmt.Fprintf(a.errOut, "warning: %s is a private key file; it is referenced, not synced\n", n)
			}
			return "~/.ssh/" + n, nil
		}
		return "", fmt.Errorf("key %q not found in %s or %s (see `sshync keys list`)", key, a.paths.KeysDir, a.paths.SSHDir)
	}
	return key, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func listCmd(a *app) *cobra.Command {
	var asJSON, all bool
	var scopeOf func() store.Scope
	cmd := &cobra.Command{
		Use:     "list [pattern]",
		Aliases: []string{"ls", "search"},
		Short:   "List hosts; pattern is a substring or glob matched against alias, hostname and user",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pat := ""
			if len(args) == 1 {
				pat = strings.ToLower(args[0])
			}
			entries := a.st.Hosts()
			if all {
				entries = a.st.Entries
			}
			type row struct {
				Alias, HostName, User, Port, Identity string
				Scope                                 store.Scope
				File                                  string
			}
			var rows []row
			for _, e := range entries {
				if s := scopeOf(); s != "" && e.Scope != s {
					continue
				}
				r := row{e.Alias, e.Block.First("HostName"), e.Block.First("User"), e.Block.First("Port"),
					strings.Join(e.Block.Get("IdentityFile"), ","), e.Scope, e.Path}
				if pat != "" && !matches(pat, r.Alias, r.HostName, r.User) {
					continue
				}
				rows = append(rows, r)
			}
			sort.SliceStable(rows, func(i, j int) bool { return strings.ToLower(rows[i].Alias) < strings.ToLower(rows[j].Alias) })
			if asJSON {
				enc := json.NewEncoder(a.out)
				enc.SetIndent("", "  ")
				if rows == nil {
					rows = []row{}
				}
				return enc.Encode(rows)
			}
			tw := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ALIAS\tHOSTNAME\tUSER\tPORT\tIDENTITY\tSCOPE")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Alias, r.HostName, r.User, r.Port, shortKey(r.Identity), r.Scope)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	cmd.Flags().BoolVarP(&all, "all", "a", false, "include wildcard and Match blocks")
	scopeOf = scopeFlags(cmd)
	return cmd
}

func matches(pat string, fields ...string) bool {
	for _, f := range fields {
		f = strings.ToLower(f)
		if strings.ContainsAny(pat, "*?[") {
			if ok, _ := path.Match(pat, f); ok {
				return true
			}
		} else if strings.Contains(f, pat) {
			return true
		}
	}
	return false
}

func shortKey(s string) string {
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = path.Base(strings.ReplaceAll(strings.Trim(p, "\""), "\\", "/"))
	}
	if s == "" {
		return ""
	}
	return strings.Join(parts, ",")
}

func showCmd(a *app) *cobra.Command {
	var effective bool
	cmd := &cobra.Command{
		Use:   "show <alias>",
		Short: "Show a host's block and where it lives (--effective: what ssh will actually use)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			es := a.st.Find(args[0], "")
			if len(es) == 0 {
				return fmt.Errorf("no host %q", args[0])
			}
			for i, e := range es {
				if i > 0 {
					a.printf("\n")
				}
				a.printf("# %s (%s)\n%s", e.Path, e.Scope, e.Block.Text("\n"))
			}
			if len(es) > 1 {
				a.printf("\nnote: defined %d times; for each option ssh uses the first value it reads (local before shared)\n", len(es))
			}
			if effective {
				out, err := exec.Command("ssh", "-F", a.paths.Config, "-G", args[0]).Output()
				if err != nil {
					return fmt.Errorf("ssh -G: %w", err)
				}
				a.printf("\n# ssh -G %s\n%s", args[0], out)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&effective, "effective", "e", false, "also print `ssh -G <alias>`")
	return cmd
}
