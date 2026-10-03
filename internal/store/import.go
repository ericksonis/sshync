package store

import (
	"strings"

	"github.com/ericksonis/sshync/internal/sshconf"
)

// Conflict is an incoming host whose options differ from an existing entry.
type Conflict struct {
	Alias    string
	Existing *Entry
	Incoming *sshconf.Block
}

type ImportResult struct {
	Added     []*Entry
	Skipped   []string // identical to an existing entry
	Conflicts []Conflict
	Preamble  string // top-level options (before the first Host) to keep in the bootstrap
}

// Import splits a monolithic config into per-host entries in scope.
// Hosts that already exist with identical options are skipped; differing ones
// are returned as conflicts and left untouched.
func (s *Store) Import(f *sshconf.File, scope Scope) (ImportResult, error) {
	var r ImportResult
	var pre []string
	for _, l := range f.Pre.Body {
		if l.Kind == sshconf.KV {
			pre = append(pre, strings.TrimSpace(l.Raw))
		}
	}
	r.Preamble = strings.Join(pre, "\n")

	for _, blk := range f.Blocks {
		alias := strings.Join(blk.Patterns(), " ")
		if blk.IsMatch() {
			alias = "Match " + blk.Header.Value
		}
		var existing []*Entry
		if blk.IsWildcard() {
			for _, e := range s.Entries {
				if e.Alias == alias {
					existing = append(existing, e)
				}
			}
		} else {
			existing = s.Find(blk.Patterns()[0], "")
		}
		if len(existing) == 0 {
			e, err := s.Put(blk, scope)
			if err != nil {
				return r, err
			}
			r.Added = append(r.Added, e)
			continue
		}
		same := false
		for _, e := range existing {
			if sshconf.Equivalent(e.Block, blk) {
				same = true
				break
			}
		}
		if same {
			r.Skipped = append(r.Skipped, alias)
		} else {
			r.Conflicts = append(r.Conflicts, Conflict{Alias: alias, Existing: existing[0], Incoming: blk})
		}
	}
	return r, nil
}

// Replace overwrites an existing entry's block with incoming, in place.
func (s *Store) Replace(e *Entry, incoming *sshconf.Block) error {
	incoming.TrimTrailing()
	for i, b := range e.File.Blocks {
		if b == e.Block {
			// keep the blank separator that followed the old block
			if n := len(b.Body); n > 0 && b.Body[n-1].Kind == sshconf.Blank && i < len(e.File.Blocks)-1 {
				incoming.Body = append(incoming.Body, b.Body[n-1])
			}
			e.File.Blocks[i] = incoming
		}
	}
	e.Block = incoming
	return s.Save(e)
}
