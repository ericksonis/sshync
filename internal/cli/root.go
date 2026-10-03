// Package cli implements the sshync command line.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ericksonis/sshync/internal/gitsync"
	"github.com/ericksonis/sshync/internal/store"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var Version = "dev"

// app carries state shared by commands.
type app struct {
	out, errOut io.Writer
	in          io.Reader
	paths       store.Paths
	st          *store.Store
	set         store.Settings
	interactive bool
}

func (a *app) open() error {
	p, err := store.DefaultPaths()
	if err != nil {
		return err
	}
	a.paths = p
	st, err := store.Open(p)
	if err != nil {
		return err
	}
	a.st = st
	a.set, err = p.LoadSettings()
	if err != nil {
		return fmt.Errorf("%s: %w", p.Settings, err)
	}
	return nil
}

func (a *app) repo() gitsync.Repo { return gitsync.Repo{Dir: a.paths.Repo} }

// changed commits repo changes (if autocommit) and pushes (if autopush).
func (a *app) changed(path, msg string) {
	if !a.st.InRepo(path) || !a.set.Sync.AutoCommit {
		return
	}
	if _, err := a.repo().CommitAll("sshync: " + msg); err != nil {
		fmt.Fprintln(a.errOut, "warning: commit failed:", err)
		return
	}
	if a.set.Sync.AutoPush && a.repo().HasRemote() {
		if _, err := a.repo().Sync("sshync: "+msg, gitsync.PreferNone); err != nil {
			fmt.Fprintln(a.errOut, "warning: push failed (run `sshync sync`):", err)
		}
	}
}

func (a *app) printf(format string, args ...any) { fmt.Fprintf(a.out, format, args...) }

// scopeFlags adds --local/--shared and returns a getter.
func scopeFlags(cmd *cobra.Command) func() store.Scope {
	var local, shared bool
	cmd.Flags().BoolVar(&local, "local", false, "only this machine (not synced)")
	cmd.Flags().BoolVar(&shared, "shared", false, "the synced repo")
	cmd.MarkFlagsMutuallyExclusive("local", "shared")
	return func() store.Scope {
		switch {
		case local:
			return store.Local
		case shared:
			return store.Shared
		}
		return ""
	}
}

func NewRoot() *cobra.Command {
	return newRoot(&app{out: os.Stdout, errOut: os.Stderr, in: os.Stdin,
		interactive: term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))})
}

func newRoot(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:           "sshync",
		Short:         "Manage and sync your OpenSSH client config",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Name() == "init" || cmd.Name() == "help" || cmd.Name() == "version" ||
				(cmd.Parent() != nil && cmd.Parent().Name() == "completion") || cmd.Name() == "completion" {
				return nil
			}
			return a.open()
		},
	}
	root.SetOut(a.out)
	root.AddCommand(
		initCmd(a), importCmd(a), addCmd(a), listCmd(a), showCmd(a),
		setCmd(a), unsetCmd(a), toggleCmd(a), fwdCmd(a), rmCmd(a), mvCmd(a),
		renameCmd(a), editCmd(a), keysCmd(a), syncCmd(a), doctorCmd(a),
	)
	return root
}

func Execute() int {
	if err := NewRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "sshync:", err)
		if errors.Is(err, store.ErrNotInit) {
			return 2
		}
		return 1
	}
	return 0
}

func joinArgs(args []string) string { return strings.Join(args, " ") }
