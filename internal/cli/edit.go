package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/ericksonis/sshync/internal/sshconf"
	"github.com/ericksonis/sshync/internal/store"
	"github.com/spf13/cobra"
)

// entryCmd builds a command that edits one host's block.
func entryCmd(a *app, use, short string, args cobra.PositionalArgs,
	run func(e *store.Entry, cmd *cobra.Command, args []string) (string, error)) *cobra.Command {
	var scopeOf func() store.Scope
	cmd := &cobra.Command{
		Use: use, Short: short, Args: args,
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := a.st.One(args[0], scopeOf())
			if err != nil {
				return err
			}
			msg, err := run(e, cmd, args[1:])
			if err != nil || msg == "" {
				return err
			}
			if err := a.st.Save(e); err != nil {
				return err
			}
			a.printf("%s\n", msg)
			a.changed(e.Path, msg)
			return nil
		},
	}
	scopeOf = scopeFlags(cmd)
	return cmd
}

func checkKey(k string, force bool) (string, error) {
	if strings.EqualFold(k, "Host") || strings.EqualFold(k, "Match") {
		return "", errors.New("use `sshync rename` to change the Host line")
	}
	if !sshconf.KnownKey(k) && !force {
		return "", fmt.Errorf("unknown ssh_config keyword %q (use --force to set it anyway)", k)
	}
	return sshconf.CanonicalKey(k), nil
}

func setCmd(a *app) *cobra.Command {
	var add, force bool
	cmd := entryCmd(a, "set <alias> <Key> <value...>",
		"Set an option (replaces existing values; --add appends for repeatable keys)",
		cobra.MinimumNArgs(3),
		func(e *store.Entry, cmd *cobra.Command, args []string) (string, error) {
			k, err := checkKey(args[0], force)
			if err != nil {
				return "", err
			}
			v := joinArgs(args[1:])
			if add {
				if !sshconf.Repeatable(k) {
					return "", fmt.Errorf("%s takes a single value; drop --add", k)
				}
				e.Block.Add(k, v)
				return fmt.Sprintf("%s: added %s %s", e.Alias, k, v), nil
			}
			if !e.Block.Set(k, v) {
				a.printf("%s: %s already %s\n", e.Alias, k, v)
				return "", nil
			}
			return fmt.Sprintf("%s: set %s %s", e.Alias, k, v), nil
		})
	cmd.Flags().BoolVar(&add, "add", false, "append another value (IdentityFile, LocalForward, ...)")
	cmd.Flags().BoolVar(&force, "force", false, "allow keywords sshync doesn't know")
	return cmd
}

func unsetCmd(a *app) *cobra.Command {
	return entryCmd(a, "unset <alias> <Key> [value...]",
		"Remove an option (all values, or only the given one)",
		cobra.MinimumNArgs(2),
		func(e *store.Entry, cmd *cobra.Command, args []string) (string, error) {
			k := sshconf.CanonicalKey(args[0])
			v := joinArgs(args[1:])
			if n := e.Block.Unset(k, v); n == 0 {
				return "", fmt.Errorf("%s has no %s %s", e.Alias, k, v)
			}
			return strings.TrimSpace(fmt.Sprintf("%s: removed %s %s", e.Alias, k, v)), nil
		})
}

func toggleCmd(a *app) *cobra.Command {
	var on, off bool
	cmd := entryCmd(a, "toggle <alias> <Key>",
		"Flip a yes/no option (e.g. IdentitiesOnly, ForwardAgent); unset counts as no",
		cobra.ExactArgs(2),
		func(e *store.Entry, cmd *cobra.Command, args []string) (string, error) {
			k, err := checkKey(args[0], false)
			if err != nil {
				return "", err
			}
			v, changed, err := toggle(e, k, on, off)
			if err != nil {
				return "", err
			}
			if !changed {
				a.printf("%s: %s already %s\n", e.Alias, k, v)
				return "", nil
			}
			return fmt.Sprintf("%s: %s %s", e.Alias, k, v), nil
		})
	cmd.Flags().BoolVar(&on, "on", false, "force yes")
	cmd.Flags().BoolVar(&off, "off", false, "force no")
	cmd.MarkFlagsMutuallyExclusive("on", "off")
	return cmd
}

// toggle flips (or with on/off forces) a yes/no option; unset counts as no.
func toggle(e *store.Entry, k string, on, off bool) (v string, changed bool, err error) {
	cur := strings.ToLower(e.Block.First(k))
	if cur != "" && cur != "yes" && cur != "no" {
		return "", false, fmt.Errorf("%s is %q, not yes/no; use `sshync set`", k, cur)
	}
	v = "yes"
	switch {
	case on:
	case off:
		v = "no"
	case cur == "yes":
		v = "no"
	}
	return v, e.Block.Set(k, v), nil
}

// forwardValue converts ssh -L/-R syntax ([bind:]port:host:hostport) into
// ssh_config syntax ("[bind:]port host:hostport").
func forwardValue(spec string) (string, error) {
	if strings.ContainsAny(spec, " \t") {
		return spec, nil
	}
	p := strings.Split(spec, ":")
	switch len(p) {
	case 3:
		return p[0] + " " + p[1] + ":" + p[2], nil
	case 4:
		return p[0] + ":" + p[1] + " " + p[2] + ":" + p[3], nil
	}
	return "", fmt.Errorf("forward %q: want [bind:]port:host:hostport", spec)
}

func fwdCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "fwd", Short: "Add, remove, or list port forwards"}
	var specs = func(cmd *cobra.Command) [][2]string {
		var out [][2]string
		for _, x := range [][2]string{{"L", "LocalForward"}, {"R", "RemoteForward"}, {"D", "DynamicForward"}} {
			vals, _ := cmd.Flags().GetStringArray(x[0])
			for _, v := range vals {
				out = append(out, [2]string{x[1], v})
			}
		}
		return out
	}
	addFlags := func(c *cobra.Command) *cobra.Command {
		c.Flags().StringArrayP("L", "L", nil, "local forward [bind:]port:host:hostport")
		c.Flags().StringArrayP("R", "R", nil, "remote forward [bind:]port:host:hostport")
		c.Flags().StringArrayP("D", "D", nil, "dynamic (SOCKS) forward [bind:]port")
		return c
	}
	apply := func(remove bool) func(e *store.Entry, cmd *cobra.Command, args []string) (string, error) {
		return func(e *store.Entry, cmd *cobra.Command, args []string) (string, error) {
			s := specs(cmd)
			if len(s) == 0 {
				return "", errors.New("give at least one of -L, -R, -D")
			}
			var done []string
			for _, kv := range s {
				v := kv[1]
				if kv[0] != "DynamicForward" {
					var err error
					if v, err = forwardValue(v); err != nil {
						return "", err
					}
				}
				if remove {
					if e.Block.Unset(kv[0], v) == 0 {
						return "", fmt.Errorf("%s has no %s %s", e.Alias, kv[0], v)
					}
				} else {
					e.Block.Add(kv[0], v)
				}
				done = append(done, kv[0]+" "+v)
			}
			verb := "added"
			if remove {
				verb = "removed"
			}
			return fmt.Sprintf("%s: %s %s", e.Alias, verb, strings.Join(done, ", ")), nil
		}
	}
	cmd.AddCommand(
		addFlags(entryCmd(a, "add <alias>", "Add forwards", cobra.ExactArgs(1), apply(false))),
		addFlags(entryCmd(a, "rm <alias>", "Remove forwards", cobra.ExactArgs(1), apply(true))),
		entryCmd(a, "list <alias>", "List forwards", cobra.ExactArgs(1),
			func(e *store.Entry, cmd *cobra.Command, args []string) (string, error) {
				for _, k := range []string{"LocalForward", "RemoteForward", "DynamicForward"} {
					for _, v := range e.Block.Get(k) {
						a.printf("%s %s\n", k, v)
					}
				}
				return "", nil
			}),
	)
	return cmd
}

func confirm(a *app, q string) (bool, error) {
	if !a.interactive {
		return false, errors.New("refusing without a terminal; pass -y")
	}
	ok := false
	err := huh.NewConfirm().Title(q).Value(&ok).Run()
	return ok, err
}

func rmCmd(a *app) *cobra.Command {
	var yes bool
	var scopeOf func() store.Scope
	cmd := &cobra.Command{
		Use: "rm <alias>", Aliases: []string{"remove"}, Short: "Remove a host", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			e, err := a.st.One(args[0], scopeOf())
			if err != nil {
				return err
			}
			if !yes {
				a.printf("%s", e.Block.Text("\n"))
				if ok, err := confirm(a, "Remove "+e.Alias+"?"); err != nil || !ok {
					return err
				}
			}
			if err := a.st.Remove(e); err != nil {
				return err
			}
			a.printf("removed %s\n", e.Alias)
			a.changed(e.Path, "remove "+e.Alias)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask")
	scopeOf = scopeFlags(cmd)
	return cmd
}

func mvCmd(a *app) *cobra.Command {
	var to string
	cmd := &cobra.Command{
		Use: "mv <alias> --to local|shared", Short: "Move a host between the synced repo and this machine",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dest, err := parseScope(to)
			if err != nil {
				return err
			}
			src := store.Local
			if dest == store.Local {
				src = store.Shared
			}
			e, err := a.st.One(args[0], src)
			if err != nil {
				return err
			}
			if e.Block.IsWildcard() {
				return errors.New("wildcard blocks: edit the defaults files with `sshync edit`")
			}
			blk := e.Block
			if err := a.st.Remove(e); err != nil {
				return err
			}
			ne, err := a.st.Put(blk, dest)
			if err != nil {
				return err
			}
			if dest == store.Shared {
				for _, f := range blk.Get("IdentityFile") {
					if !strings.Contains(f, "sshync/repo/keys/") {
						fmt.Fprintf(a.errOut, "note: IdentityFile %s is not in the repo; it must exist on every machine\n", f)
					}
				}
			}
			a.printf("moved %s to %s (%s)\n", e.Alias, dest, ne.Path)
			a.changed(a.paths.HostsDir, "move "+e.Alias+" to "+string(dest))
			return nil
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "local or shared")
	cmd.MarkFlagRequired("to")
	return cmd
}

func renameCmd(a *app) *cobra.Command {
	var scopeOf func() store.Scope
	cmd := &cobra.Command{
		Use: "rename <alias> <new-alias>", Short: "Rename a host alias", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			old, nw := args[0], args[1]
			e, err := a.st.One(old, scopeOf())
			if err != nil {
				return err
			}
			if len(a.st.Find(nw, "")) > 0 {
				return fmt.Errorf("host %q already exists", nw)
			}
			pats := e.Block.Patterns()
			for i, p := range pats {
				if p == old {
					pats[i] = nw
				}
			}
			for i := range pats {
				pats[i] = sshconf.Quote(pats[i])
			}
			e.Block.Header.Value = strings.Join(pats, " ")
			e.Block.Header.Raw = e.Block.Header.Indent + e.Block.Header.Key + e.Block.Header.Sep + e.Block.Header.Value + e.Block.Header.Trailing
			oldPath := e.Path
			if len(e.File.Blocks) == 1 && filepath.Base(e.Path) == store.FileName(old) {
				e.Path = filepath.Join(filepath.Dir(e.Path), store.FileName(nw))
			}
			if err := a.st.Save(e); err != nil {
				return err
			}
			if oldPath != e.Path {
				if err := os.Remove(oldPath); err != nil {
					return err
				}
			}
			e.Alias = strings.Join(e.Block.Patterns(), " ")
			a.printf("renamed %s -> %s\n", old, nw)
			a.changed(e.Path, "rename "+old+" to "+nw)
			return nil
		},
	}
	scopeOf = scopeFlags(cmd)
	return cmd
}

func editor() []string {
	for _, v := range []string{"SSHYNC_EDITOR", "VISUAL", "EDITOR"} {
		if e := strings.TrimSpace(os.Getenv(v)); e != "" {
			return strings.Fields(e)
		}
	}
	if runtime.GOOS == "windows" {
		return []string{"notepad"}
	}
	return []string{"vi"}
}

func editCmd(a *app) *cobra.Command {
	var scopeOf func() store.Scope
	var defaults bool
	cmd := &cobra.Command{
		Use:   "edit [alias]",
		Short: "Open a host's file (or with --defaults, the Host * file) in $EDITOR",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var path string
			switch {
			case defaults:
				path = a.paths.Defaults
				if scopeOf() == store.Local {
					path = a.paths.LocalDefaults
				}
			case len(args) == 1:
				e, err := a.st.One(args[0], scopeOf())
				if err != nil {
					return err
				}
				path = e.Path
			default:
				return errors.New("give an alias or --defaults")
			}
			ed := editor()
			c := exec.Command(ed[0], append(ed[1:], path)...)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := c.Run(); err != nil {
				return err
			}
			a.changed(path, "edit "+filepath.Base(path))
			return nil
		},
	}
	cmd.Flags().BoolVar(&defaults, "defaults", false, "edit the wildcard defaults file (shared, or local with --local)")
	scopeOf = scopeFlags(cmd)
	return cmd
}
