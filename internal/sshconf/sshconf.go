// Package sshconf is a small, lossless parser/writer for OpenSSH client
// config files. Unmodified lines are written back byte-for-byte; edited or
// inserted lines copy the indentation and separator style of their neighbors.
package sshconf

import (
	"strings"
)

type Kind int

const (
	Blank Kind = iota
	Comment
	KV
)

// Line is one physical line. For KV lines Raw == Indent+Key+Sep+Value+Trailing.
type Line struct {
	Raw      string
	Kind     Kind
	Indent   string
	Key      string
	Sep      string
	Value    string
	Trailing string
}

func (l *Line) rebuild() {
	l.Raw = l.Indent + l.Key + l.Sep + l.Value + l.Trailing
}

// IsKey reports whether l is a KV line with the given keyword (case-insensitive).
func (l *Line) IsKey(key string) bool {
	return l.Kind == KV && strings.EqualFold(l.Key, key)
}

// Block is a Host/Match section. The preamble (lines before the first
// Host/Match) is a Block with a nil Header.
type Block struct {
	Header *Line
	Body   []*Line
}

type File struct {
	Pre          *Block
	Blocks       []*Block
	EOL          string
	FinalNewline bool
}

func parseLine(raw string) *Line {
	l := &Line{Raw: raw}
	trimmed := strings.TrimLeft(raw, " \t")
	l.Indent = raw[:len(raw)-len(trimmed)]
	switch {
	case strings.TrimSpace(trimmed) == "":
		l.Kind = Blank
		return l
	case strings.HasPrefix(trimmed, "#"):
		l.Kind = Comment
		return l
	}
	l.Kind = KV
	i := strings.IndexAny(trimmed, " \t=")
	if i < 0 {
		l.Key = trimmed
		return l
	}
	l.Key = trimmed[:i]
	rest := trimmed[i:]
	j := 0
	sawEq := false
	for j < len(rest) {
		c := rest[j]
		if c == ' ' || c == '\t' {
			j++
		} else if c == '=' && !sawEq {
			sawEq = true
			j++
		} else {
			break
		}
	}
	l.Sep = rest[:j]
	val := rest[j:]
	v := strings.TrimRight(val, " \t")
	l.Value = v
	l.Trailing = val[len(v):]
	return l
}

func isHeader(l *Line) bool {
	return l.IsKey("Host") || l.IsKey("Match")
}

// Parse parses config text. It never fails; unrecognised lines are kept as KV.
func Parse(data string) *File {
	f := &File{EOL: "\n", Pre: &Block{}}
	if strings.Contains(data, "\r\n") {
		f.EOL = "\r\n"
	}
	if data == "" {
		return f
	}
	f.FinalNewline = strings.HasSuffix(data, "\n")
	body := strings.TrimSuffix(data, "\n")
	cur := f.Pre
	for _, raw := range strings.Split(body, "\n") {
		raw = strings.TrimSuffix(raw, "\r")
		l := parseLine(raw)
		if isHeader(l) {
			cur = &Block{Header: l}
			f.Blocks = append(f.Blocks, cur)
			continue
		}
		cur.Body = append(cur.Body, l)
	}
	return f
}

func (f *File) lines() []*Line {
	var out []*Line
	out = append(out, f.Pre.Body...)
	for _, b := range f.Blocks {
		out = append(out, b.Header)
		out = append(out, b.Body...)
	}
	return out
}

// String serializes the file. An unmodified parse round-trips exactly.
func (f *File) String() string {
	ls := f.lines()
	if len(ls) == 0 {
		return ""
	}
	parts := make([]string, len(ls))
	for i, l := range ls {
		parts[i] = l.Raw
	}
	s := strings.Join(parts, f.EOL)
	if f.FinalNewline {
		s += f.EOL
	}
	return s
}

// Fields splits an ssh_config argument string, honoring double quotes.
func Fields(s string) []string {
	var out []string
	var b strings.Builder
	inQ, have := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
			have = true
		case (r == ' ' || r == '\t') && !inQ:
			if have {
				out = append(out, b.String())
				b.Reset()
				have = false
			}
		default:
			b.WriteRune(r)
			have = true
		}
	}
	if have {
		out = append(out, b.String())
	}
	return out
}

// Quote wraps v in double quotes if it contains whitespace.
func Quote(v string) string {
	if strings.ContainsAny(v, " \t") && !strings.HasPrefix(v, "\"") {
		return "\"" + v + "\""
	}
	return v
}

// Patterns returns the Host patterns of a block (nil for Match/preamble).
func (b *Block) Patterns() []string {
	if b.Header == nil || !b.Header.IsKey("Host") {
		return nil
	}
	return Fields(b.Header.Value)
}

func (b *Block) IsMatch() bool { return b.Header != nil && b.Header.IsKey("Match") }

// IsWildcard reports whether the block applies to more than one literal host.
func (b *Block) IsWildcard() bool {
	if b.IsMatch() {
		return true
	}
	for _, p := range b.Patterns() {
		if strings.ContainsAny(p, "*?!") {
			return true
		}
	}
	return false
}

// HasPattern reports whether alias is one of the block's literal patterns.
func (b *Block) HasPattern(alias string) bool {
	for _, p := range b.Patterns() {
		if p == alias {
			return true
		}
	}
	return false
}

// Get returns the values of every line with the given keyword.
func (b *Block) Get(key string) []string {
	var out []string
	for _, l := range b.Body {
		if l.IsKey(key) {
			out = append(out, l.Value)
		}
	}
	return out
}

// First returns the first value of key, or "".
func (b *Block) First(key string) string {
	if v := b.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

func (b *Block) lastKV() int {
	for i := len(b.Body) - 1; i >= 0; i-- {
		if b.Body[i].Kind == KV {
			return i
		}
	}
	return -1
}

func (b *Block) newLine(key, value string) *Line {
	l := &Line{Kind: KV, Key: CanonicalKey(key), Indent: "    ", Sep: " ", Value: value}
	if i := b.lastKV(); i >= 0 {
		l.Indent, l.Sep = b.Body[i].Indent, b.Body[i].Sep
	} else if b.Header == nil {
		l.Indent = ""
	}
	l.rebuild()
	return l
}

// Add appends key/value after the last option line (or after any existing
// lines with the same key, keeping repeated options grouped).
func (b *Block) Add(key, value string) {
	at := b.lastKV()
	for i, l := range b.Body {
		if l.IsKey(key) {
			at = i
		}
	}
	nl := b.newLine(key, value)
	b.Body = append(b.Body[:at+1], append([]*Line{nl}, b.Body[at+1:]...)...)
}

// Set replaces the value of key, removing duplicates; adds it if absent.
// It reports whether the block changed.
func (b *Block) Set(key, value string) bool {
	found := false
	changed := false
	out := b.Body[:0]
	for _, l := range b.Body {
		if l.IsKey(key) {
			if found {
				changed = true
				continue
			}
			found = true
			if l.Value != value {
				l.Value = value
				l.rebuild()
				changed = true
			}
		}
		out = append(out, l)
	}
	b.Body = out
	if !found {
		b.Add(key, value)
		changed = true
	}
	return changed
}

// Unset removes lines with key. If value is non-empty only lines whose value
// matches it are removed. Returns the number of lines removed.
func (b *Block) Unset(key, value string) int {
	n := 0
	out := b.Body[:0]
	for _, l := range b.Body {
		if l.IsKey(key) && (value == "" || normalize(l.Value) == normalize(value)) {
			n++
			continue
		}
		out = append(out, l)
	}
	b.Body = out
	return n
}

func normalize(v string) string { return strings.Join(Fields(v), " ") }

// Options returns the (canonical key, value) pairs of the block, in order.
func (b *Block) Options() [][2]string {
	var out [][2]string
	for _, l := range b.Body {
		if l.Kind == KV {
			out = append(out, [2]string{CanonicalKey(l.Key), normalize(l.Value)})
		}
	}
	return out
}

// Equivalent reports whether two blocks set the same options (ignoring
// formatting, comments, and keyword case).
func Equivalent(a, b *Block) bool {
	if normalize(a.Header.Value) != normalize(b.Header.Value) {
		return false
	}
	ao, bo := a.Options(), b.Options()
	if len(ao) != len(bo) {
		return false
	}
	for i := range ao {
		if !strings.EqualFold(ao[i][0], bo[i][0]) || ao[i][1] != bo[i][1] {
			return false
		}
	}
	return true
}

// Text renders a block on its own, without trailing blank lines.
func (b *Block) Text(eol string) string {
	var parts []string
	if b.Header != nil {
		parts = append(parts, b.Header.Raw)
	}
	body := b.Body
	for len(body) > 0 && body[len(body)-1].Kind == Blank {
		body = body[:len(body)-1]
	}
	for _, l := range body {
		parts = append(parts, l.Raw)
	}
	return strings.Join(parts, eol) + eol
}

// TrimTrailing drops blank lines (including whitespace-only ones) at the end
// of the block.
func (b *Block) TrimTrailing() {
	for len(b.Body) > 0 && b.Body[len(b.Body)-1].Kind == Blank {
		b.Body = b.Body[:len(b.Body)-1]
	}
}

// NewHostBlock creates "Host alias" with no options.
func NewHostBlock(alias string) *Block {
	h := &Line{Kind: KV, Key: "Host", Sep: " ", Value: alias}
	h.rebuild()
	return &Block{Header: h}
}

// FindHost returns the first block whose patterns include alias.
func (f *File) FindHost(alias string) *Block {
	for _, b := range f.Blocks {
		if b.HasPattern(alias) {
			return b
		}
	}
	return nil
}

// RemoveBlock drops b from the file.
func (f *File) RemoveBlock(b *Block) bool {
	for i, x := range f.Blocks {
		if x == b {
			f.Blocks = append(f.Blocks[:i], f.Blocks[i+1:]...)
			return true
		}
	}
	return false
}

// AppendBlock adds b at the end, separated from the previous block by a blank line.
func (f *File) AppendBlock(b *Block) {
	last := f.Pre
	if n := len(f.Blocks); n > 0 {
		last = f.Blocks[n-1]
	}
	if len(f.Lines()) > 0 && (len(last.Body) == 0 || last.Body[len(last.Body)-1].Kind != Blank) {
		last.Body = append(last.Body, &Line{Kind: Blank})
	}
	f.Blocks = append(f.Blocks, b)
	f.FinalNewline = true
}

// Lines exposes all lines in order (read-only use).
func (f *File) Lines() []*Line { return f.lines() }
