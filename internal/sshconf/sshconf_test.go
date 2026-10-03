package sshconf

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRoundTrip(t *testing.T) {
	for _, name := range []string{"mixed.conf", "crlf.conf", "nofinal.conf"} {
		in := read(t, name)
		if out := Parse(in).String(); out != in {
			t.Errorf("%s: round trip differs\n--- in\n%q\n--- out\n%q", name, in, out)
		}
	}
	if Parse("").String() != "" {
		t.Error("empty file")
	}
}

// Set SSHYNC_REAL_CONFIG to a real config path to check it round-trips.
func TestRoundTripReal(t *testing.T) {
	p := os.Getenv("SSHYNC_REAL_CONFIG")
	if p == "" {
		t.Skip("SSHYNC_REAL_CONFIG not set")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if Parse(string(b)).String() != string(b) {
		t.Fatal("real config does not round-trip")
	}
}

func TestStructure(t *testing.T) {
	f := Parse(read(t, "mixed.conf"))
	if len(f.Blocks) != 5 {
		t.Fatalf("blocks = %d", len(f.Blocks))
	}
	if got := f.Pre.First("compression"); got != "yes" {
		t.Errorf("preamble Compression = %q", got)
	}
	beta := f.FindHost("beta-alias")
	if beta == nil || beta.First("HostName") != "beta.example.com" {
		t.Fatalf("beta hostname via '=' separator: %+v", beta)
	}
	if beta.First("port") != "2022" {
		t.Errorf("trailing whitespace not trimmed from value")
	}
	if got := beta.Get("LocalForward"); len(got) != 2 {
		t.Errorf("LocalForward = %v", got)
	}
	if q := f.FindHost("quoted name"); q == nil || !reflect.DeepEqual(Fields(q.First("IdentityFile")), []string{"C:/Program Files/key.pub"}) {
		t.Errorf("quoted host/identity")
	}
	if !f.Blocks[3].IsMatch() || !f.Blocks[4].IsWildcard() || f.Blocks[0].IsWildcard() {
		t.Errorf("match/wildcard detection")
	}
}

func TestEdits(t *testing.T) {
	f := Parse(read(t, "mixed.conf"))
	a := f.FindHost("alpha")
	a.Set("identitiesonly", "yes")
	a.Set("forwardagent", "no")
	a.Add("LocalForward", "8080 localhost:80")
	a.Set("user", "root")
	out := a.Text("\n")
	want := "Host alpha\n" +
		"    HostName alpha.example.com\n" +
		"\tUser root\n" +
		"    IdentityFile ~/.ssh/a.pub\n" +
		"    ForwardAgent no\n" +
		"    IdentitiesOnly yes\n" +
		"    LocalForward 8080 localhost:80\n"
	if out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}

	b := f.FindHost("beta")
	b.Add("LocalForward", "3402 three.example.com:3389")
	if n := b.Unset("LocalForward", "3400  one.example.com:3389"); n != 1 {
		t.Errorf("unset one forward removed %d", n)
	}
	got := b.Get("LocalForward")
	if !reflect.DeepEqual(got, []string{"3401 two.example.com:3389", "3402 three.example.com:3389"}) {
		t.Errorf("forwards = %v", got)
	}
	if b.Set("Port", "2022") {
		t.Error("Set with same value reported change")
	}
	// untouched blocks are byte-identical
	s := f.String()
	if !strings.Contains(s, "Match host *.example.com exec \"true\"\n  User matcher\n") {
		t.Error("match block altered")
	}
}

func TestSetDedupes(t *testing.T) {
	f := Parse("Host x\n  User a\n  User b\n")
	f.FindHost("x").Set("User", "c")
	if f.String() != "Host x\n  User c\n" {
		t.Errorf("got %q", f.String())
	}
}

func TestNewBlockAndAppend(t *testing.T) {
	f := Parse("Host a\n  User u\n")
	b := NewHostBlock("new")
	b.Add("hostname", "new.example.com")
	b.Add("User", "me")
	f.AppendBlock(b)
	want := "Host a\n  User u\n\nHost new\n    HostName new.example.com\n    User me\n"
	if f.String() != want {
		t.Errorf("got %q", f.String())
	}
	g := Parse("")
	g.AppendBlock(NewHostBlock("only"))
	if g.String() != "Host only\n" {
		t.Errorf("empty append got %q", g.String())
	}
}

func TestEquivalent(t *testing.T) {
	a := Parse("Host x\n    hostname h\n    User  u\n").FindHost("x")
	b := Parse("Host x\n\t# note\n\tHostName=h\n\tUser u\n").FindHost("x")
	c := Parse("Host x\n  HostName h\n  User v\n").FindHost("x")
	if !Equivalent(a, b) || Equivalent(a, c) {
		t.Error("equivalence")
	}
}
