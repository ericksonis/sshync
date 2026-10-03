// Package store maps sshync's on-disk layout (one Host block per file, split
// into a synced repo and a machine-local directory) onto sshconf files.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ericksonis/sshync/internal/sshconf"
)

type Scope string

const (
	Shared Scope = "shared"
	Local  Scope = "local"
)

type Paths struct {
	SSHDir        string // ~/.ssh
	Config        string // ~/.ssh/config (bootstrap)
	Root          string // ~/.ssh/sshync
	LocalDir      string // ~/.ssh/sshync/local.d
	LocalDefaults string // ~/.ssh/sshync/local-defaults.conf
	Repo          string // ~/.ssh/sshync/repo
	HostsDir      string // repo/hosts.d
	KeysDir       string // repo/keys
	Defaults      string // repo/defaults.conf
	Settings      string // repo/sshync.toml
}

// DefaultPaths uses %USERPROFILE%\.ssh, or $SSHYNC_SSH_DIR when set (tests).
func DefaultPaths() (Paths, error) {
	dir := os.Getenv("SSHYNC_SSH_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, err
		}
		dir = filepath.Join(home, ".ssh")
	}
	return PathsFor(dir), nil
}

func PathsFor(sshDir string) Paths {
	root := filepath.Join(sshDir, "sshync")
	repo := filepath.Join(root, "repo")
	return Paths{
		SSHDir:        sshDir,
		Config:        filepath.Join(sshDir, "config"),
		Root:          root,
		LocalDir:      filepath.Join(root, "local.d"),
		LocalDefaults: filepath.Join(root, "local-defaults.conf"),
		Repo:          repo,
		HostsDir:      filepath.Join(repo, "hosts.d"),
		KeysDir:       filepath.Join(repo, "keys"),
		Defaults:      filepath.Join(repo, "defaults.conf"),
		Settings:      filepath.Join(repo, "sshync.toml"),
	}
}

// KeyRef is how IdentityFile refers to a key in the repo. "~" in IdentityFile
// resolves to %USERPROFILE% on Win32-OpenSSH, so it is portable across machines.
func (p Paths) KeyRef(name string) string {
	return "~/.ssh/sshync/repo/keys/" + name
}

const Marker = "# Managed by sshync"

// Bootstrap renders ~/.ssh/config. Include paths are absolute because
// Win32-OpenSSH expands "~" in Include from %HOME%, which may not be the
// profile directory. Order matters: the first value obtained wins, so local
// hosts override shared ones and wildcard defaults come last.
func (p Paths) Bootstrap(preamble string) string {
	slash := func(s string) string { return sshconf.Quote(filepath.ToSlash(s)) }
	var b strings.Builder
	b.WriteString(Marker + ". Hosts live in " + filepath.ToSlash(p.Root) + "/; edit them with `sshync`.\n")
	if preamble = strings.TrimSpace(preamble); preamble != "" {
		b.WriteString("\n" + preamble + "\n\n")
	}
	b.WriteString("Include " + slash(filepath.Join(p.LocalDir, "*.conf")) + "\n")
	b.WriteString("Include " + slash(filepath.Join(p.HostsDir, "*.conf")) + "\n")
	b.WriteString("Include " + slash(p.LocalDefaults) + "\n")
	b.WriteString("Include " + slash(p.Defaults) + "\n")
	return b.String()
}

// Managed reports whether ~/.ssh/config is an sshync bootstrap.
func (p Paths) Managed() bool {
	b, err := os.ReadFile(p.Config)
	return err == nil && strings.HasPrefix(string(b), Marker)
}

func (p Paths) Initialized() bool {
	_, err := os.Stat(p.HostsDir)
	return err == nil
}

// Entry is one Host block in one file.
type Entry struct {
	Alias string
	Scope Scope
	Path  string
	File  *sshconf.File
	Block *sshconf.Block
}

// IsDefaults reports whether path is one of the wildcard/Match defaults files.
func (p Paths) IsDefaults(path string) bool {
	return path == p.Defaults || path == p.LocalDefaults
}

type Store struct {
	P       Paths
	Entries []*Entry
}

var ErrNotInit = errors.New("sshync is not initialized; run `sshync init`")

func Open(p Paths) (*Store, error) {
	if !p.Initialized() {
		return nil, ErrNotInit
	}
	s := &Store{P: p}
	return s, s.load()
}

func (s *Store) load() error {
	s.Entries = nil
	type src struct {
		glob  string
		scope Scope
	}
	for _, x := range []src{
		{filepath.Join(s.P.LocalDir, "*.conf"), Local},
		{filepath.Join(s.P.HostsDir, "*.conf"), Shared},
		{s.P.LocalDefaults, Local},
		{s.P.Defaults, Shared},
	} {
		paths, err := filepath.Glob(x.glob)
		if err != nil {
			return err
		}
		sort.Strings(paths)
		for _, path := range paths {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			f := sshconf.Parse(string(b))
			for _, blk := range f.Blocks {
				alias := strings.Join(blk.Patterns(), " ")
				if blk.IsMatch() {
					alias = "Match " + blk.Header.Value
				}
				s.Entries = append(s.Entries, &Entry{Alias: alias, Scope: x.scope, Path: path, File: f, Block: blk})
			}
		}
	}
	return nil
}

// Find returns entries with a literal pattern equal to alias, optionally
// restricted to one scope ("" = any). Local entries come first.
func (s *Store) Find(alias string, scope Scope) []*Entry {
	var out []*Entry
	for _, e := range s.Entries {
		if (scope == "" || e.Scope == scope) && (e.Block.HasPattern(alias) || e.Alias == alias) {
			out = append(out, e)
		}
	}
	return out
}

// One resolves alias to a single entry, erroring when absent or ambiguous.
func (s *Store) One(alias string, scope Scope) (*Entry, error) {
	es := s.Find(alias, scope)
	switch len(es) {
	case 0:
		return nil, fmt.Errorf("no host %q", alias)
	case 1:
		return es[0], nil
	}
	var where []string
	for _, e := range es {
		where = append(where, fmt.Sprintf("%s (%s)", e.Path, e.Scope))
	}
	return nil, fmt.Errorf("host %q is defined in several places; pass --local or --shared:\n  %s", alias, strings.Join(where, "\n  "))
}

// Hosts returns non-wildcard entries.
func (s *Store) Hosts() []*Entry {
	var out []*Entry
	for _, e := range s.Entries {
		if !e.Block.IsWildcard() {
			out = append(out, e)
		}
	}
	return out
}

var unsafe = regexp.MustCompile(`[^A-Za-z0-9._@-]+`)

// FileName maps an alias to its per-host file name.
func FileName(alias string) string {
	n := unsafe.ReplaceAllString(alias, "_")
	n = strings.Trim(n, ".")
	if n == "" {
		n = "_"
	}
	return n + ".conf"
}

func (s *Store) dirFor(scope Scope) string {
	if scope == Local {
		return s.P.LocalDir
	}
	return s.P.HostsDir
}

func (s *Store) defaultsFor(scope Scope) string {
	if scope == Local {
		return s.P.LocalDefaults
	}
	return s.P.Defaults
}

// Put writes block as a new entry in scope. Wildcard/Match blocks are appended
// to that scope's defaults file; literal hosts get their own file.
func (s *Store) Put(block *sshconf.Block, scope Scope) (*Entry, error) {
	var path string
	var f *sshconf.File
	block.TrimTrailing()
	if block.IsWildcard() {
		path = s.defaultsFor(scope)
		f = s.fileAt(path)
	} else {
		pats := block.Patterns()
		if len(pats) == 0 {
			return nil, errors.New("block has no Host pattern")
		}
		path = freePath(s.dirFor(scope), FileName(pats[0]))
		f = sshconf.Parse("")
	}
	f.AppendBlock(block)
	e := &Entry{Alias: strings.Join(block.Patterns(), " "), Scope: scope, Path: path, File: f, Block: block}
	if err := s.Save(e); err != nil {
		return nil, err
	}
	s.Entries = append(s.Entries, e)
	return e, nil
}

// freePath returns dir/name, or dir/name-N.conf if taken (e.g. aliases that
// differ only in case on a case-insensitive filesystem).
func freePath(dir, name string) string {
	base := strings.TrimSuffix(name, ".conf")
	p := filepath.Join(dir, name)
	for i := 2; ; i++ {
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s-%d.conf", base, i))
	}
}

func (s *Store) fileAt(path string) *sshconf.File {
	for _, e := range s.Entries {
		if e.Path == path {
			return e.File
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return sshconf.Parse("")
	}
	return sshconf.Parse(string(b))
}

// Save writes the entry's file.
func (s *Store) Save(e *Entry) error {
	if err := os.MkdirAll(filepath.Dir(e.Path), 0o700); err != nil {
		return err
	}
	return WriteAtomic(e.Path, []byte(e.File.String()))
}

// Remove deletes the entry; the file is removed when it has no blocks left.
func (s *Store) Remove(e *Entry) error {
	e.File.RemoveBlock(e.Block)
	for i, x := range s.Entries {
		if x == e {
			s.Entries = append(s.Entries[:i], s.Entries[i+1:]...)
			break
		}
	}
	if len(e.File.Blocks) == 0 && !s.P.IsDefaults(e.Path) {
		return os.Remove(e.Path)
	}
	return s.Save(e)
}

// WriteAtomic writes via a temp file + rename so a crash never leaves a
// half-written config behind.
func WriteAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".sshync-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Keys lists *.pub files in the repo's key directory.
func (s *Store) Keys() []string {
	m, _ := filepath.Glob(filepath.Join(s.P.KeysDir, "*.pub"))
	var out []string
	for _, p := range m {
		out = append(out, filepath.Base(p))
	}
	sort.Strings(out)
	return out
}

// InRepo reports whether path is inside the synced repo.
func (s *Store) InRepo(path string) bool {
	rel, err := filepath.Rel(s.P.Repo, path)
	return err == nil && !strings.HasPrefix(rel, "..")
}
