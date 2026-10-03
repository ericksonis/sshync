package cli

import (
	"sort"
	"strings"

	"github.com/ericksonis/sshync/internal/sshconf"
	"github.com/ericksonis/sshync/internal/store"
	"github.com/spf13/cobra"
)

type compFunc = func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective)

const noFiles = cobra.ShellCompDirectiveNoFileComp

// ready opens the store for completion; errors (e.g. not initialised) just
// mean no suggestions.
func (a *app) ready() bool {
	return a.st != nil || a.open() == nil
}

func prefixed(vals []string, prefix string) []string {
	var out []string
	for _, v := range vals {
		name, _, _ := strings.Cut(v, "\t")
		if strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			out = append(out, v)
		}
	}
	return out
}

// aliases returns "alias\thostname" for every literal host pattern.
func (a *app) aliases() []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range a.st.Hosts() {
		for _, p := range e.Block.Patterns() {
			if seen[p] || strings.ContainsAny(p, "*?!") {
				continue
			}
			seen[p] = true
			desc := e.Block.First("HostName")
			if e.Scope == store.Local {
				desc = strings.TrimSpace(desc + " (local)")
			}
			out = append(out, p+"\t"+desc)
		}
	}
	sort.Strings(out)
	return out
}

// completeAliasThen completes a host alias as the first argument, then calls
// next with that host's entry and the remaining arguments.
func completeAliasThen(a *app, next func(e *store.Entry, rest []string) []string) compFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if !a.ready() {
			return nil, noFiles
		}
		if len(args) == 0 {
			return prefixed(a.aliases(), toComplete), noFiles
		}
		if next == nil {
			return nil, noFiles
		}
		es := a.st.Find(args[0], "")
		if len(es) == 0 {
			return nil, noFiles
		}
		return prefixed(next(es[0], args[1:]), toComplete), noFiles
	}
}

func fixed(vals ...string) compFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return prefixed(vals, toComplete), noFiles
	}
}

// identityRefs lists IdentityFile values: repo keys first, then ~/.ssh/*.pub.
func (a *app) identityRefs() []string {
	var out []string
	for _, k := range keyChoices(a) {
		if strings.HasPrefix(k, "~/") {
			out = append(out, k)
		} else {
			out = append(out, a.paths.KeyRef(k))
		}
	}
	return out
}

// presentKeys lists the keywords set in a block, without duplicates.
func presentKeys(b *sshconf.Block) []string {
	seen := map[string]bool{}
	var out []string
	for _, kv := range b.Options() {
		if k := sshconf.CanonicalKey(kv[0]); !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// registerCompletions wires dynamic completion into the command tree.
func registerCompletions(a *app, root *cobra.Command) {
	set := func(path string, f compFunc) {
		if c, _, err := root.Find(strings.Fields(path)); err == nil && c != root {
			c.ValidArgsFunction = f
		}
	}
	flag := func(path, name string, f compFunc) {
		if c, _, err := root.Find(strings.Fields(path)); err == nil && c != root {
			_ = c.RegisterFlagCompletionFunc(name, f)
		}
	}
	for _, p := range []string{"show", "rm", "mv", "rename", "edit", "fwd add", "fwd list"} {
		set(p, completeAliasThen(a, nil))
	}
	set("set", completeAliasThen(a, func(e *store.Entry, rest []string) []string {
		switch {
		case len(rest) == 0:
			return sshconf.Keywords()
		case len(rest) == 1 && strings.EqualFold(rest[0], "IdentityFile"):
			return a.identityRefs()
		case len(rest) == 1 && sshconf.KnownKey(rest[0]) && contains(sshconf.YesNo, sshconf.CanonicalKey(rest[0])):
			return []string{"yes", "no"}
		}
		return nil
	}))
	set("unset", completeAliasThen(a, func(e *store.Entry, rest []string) []string {
		if len(rest) == 0 {
			return presentKeys(e.Block)
		}
		if len(rest) == 1 && sshconf.Repeatable(rest[0]) {
			return e.Block.Get(rest[0])
		}
		return nil
	}))
	set("toggle", completeAliasThen(a, func(e *store.Entry, rest []string) []string {
		if len(rest) == 0 {
			return sshconf.YesNo
		}
		return nil
	}))
	set("fwd rm", completeAliasThen(a, nil))
	fwdValues := func(key string) compFunc {
		return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 || !a.ready() {
				return nil, noFiles
			}
			var out []string
			for _, e := range a.st.Find(args[0], "") {
				for _, v := range e.Block.Get(key) {
					// back to the -L/-R command-line form
					out = append(out, strings.Replace(v, " ", ":", 1))
				}
			}
			return prefixed(out, toComplete), noFiles
		}
	}
	flag("fwd rm", "L", fwdValues("LocalForward"))
	flag("fwd rm", "R", fwdValues("RemoteForward"))
	flag("fwd rm", "D", fwdValues("DynamicForward"))
	flag("add", "key", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if !a.ready() {
			return nil, noFiles
		}
		return prefixed(keyChoices(a), toComplete), noFiles
	})
	flag("add", "option", fixed(withSuffix(sshconf.Keywords(), "=")...))
	flag("mv", "to", fixed("local", "shared"))
	flag("init", "to", fixed("shared", "local"))
	flag("import", "to", fixed("shared", "local"))
	flag("init", "prefer", fixed("repo", "mine"))
	flag("import", "prefer", fixed("repo", "mine"))
	flag("sync", "prefer", fixed("mine", "remote"))
	for _, p := range []string{"sync", "doctor", "keys list", "keys agent"} {
		set(p, cobra.NoFileCompletions)
	}
	set("list", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 || !a.ready() {
			return nil, noFiles
		}
		return prefixed(a.aliases(), toComplete), noFiles
	})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func withSuffix(list []string, suffix string) []string {
	out := make([]string, len(list))
	for i, v := range list {
		out[i] = v + suffix
	}
	return out
}
