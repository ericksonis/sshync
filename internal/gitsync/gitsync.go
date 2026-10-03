// Package gitsync drives the installed git binary, so the user's existing
// credentials and ssh-agent (Bitwarden) are used for the remote.
package gitsync

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Repo struct{ Dir string }

func (r Repo) Git(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", r.Dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		return out.String(), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, msg)
	}
	return strings.TrimSpace(out.String()), nil
}

func Init(dir string) error {
	_, err := Repo{dir}.Git("init", "-q", "-b", "main")
	return err
}

func Clone(url, dir string) error {
	cmd := exec.Command("git", "clone", "-q", url, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CommitAll stages everything and commits; it reports whether a commit was made.
func (r Repo) CommitAll(msg string) (bool, error) {
	if _, err := r.Git("add", "-A"); err != nil {
		return false, err
	}
	if _, err := r.Git("diff", "--cached", "--quiet"); err == nil {
		return false, nil
	}
	if _, err := r.Git("commit", "-q", "-m", msg); err != nil {
		return false, err
	}
	return true, nil
}

func (r Repo) HasRemote() bool {
	out, err := r.Git("remote")
	return err == nil && out != ""
}

func (r Repo) Status() (string, error) { return r.Git("status", "--short", "--branch") }

// Prefer picks a side when the same file changed on both machines.
type Prefer string

const (
	PreferNone   Prefer = ""
	PreferMine   Prefer = "mine"
	PreferRemote Prefer = "remote"
)

// ConflictError lists files changed on both sides. The rebase has been
// aborted, so the working tree (and therefore ssh) is left valid.
type ConflictError struct{ Files []string }

func (e *ConflictError) Error() string {
	return "these files changed on both machines: " + strings.Join(e.Files, ", ") +
		"\nlocal state kept; re-run with `sshync sync --prefer mine` or `--prefer remote`"
}

func (r Repo) rebaseInProgress() bool {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if p, err := r.Git("rev-parse", "--git-path", d); err == nil {
			if !strings.HasPrefix(p, "/") && !strings.Contains(p, ":") {
				p = r.Dir + "/" + p
			}
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
	}
	return false
}

func (r Repo) pullRebase(prefer Prefer, extra ...string) error {
	args := []string{"pull", "--rebase", "-q"}
	// During a rebase "ours" is the upstream and "theirs" the local commits.
	switch prefer {
	case PreferMine:
		args = append(args, "-X", "theirs")
	case PreferRemote:
		args = append(args, "-X", "ours")
	}
	_, err := r.Git(append(args, extra...)...)
	if err == nil {
		return nil
	}
	if !r.rebaseInProgress() {
		return err
	}
	files, _ := r.Git("diff", "--name-only", "--diff-filter=U")
	if _, aerr := r.Git("rebase", "--abort"); aerr != nil {
		return fmt.Errorf("%v; additionally `git rebase --abort` failed: %v", err, aerr)
	}
	return &ConflictError{Files: strings.Fields(files)}
}

// Sync commits local changes, rebases onto the remote, and pushes.
func (r Repo) Sync(msg string, prefer Prefer) (string, error) {
	var log []string
	if r.rebaseInProgress() {
		return "", fmt.Errorf("a git rebase is in progress in %s; finish or abort it first (git -C %q rebase --abort)", r.Dir, r.Dir)
	}
	if ok, err := r.CommitAll(msg); err != nil {
		return "", err
	} else if ok {
		log = append(log, "committed local changes")
	}
	if !r.HasRemote() {
		return strings.Join(log, "\n"), fmt.Errorf("repo %s has no remote; add one with: git -C %q remote add origin <url>", r.Dir, r.Dir)
	}
	if _, err := r.Git("rev-parse", "--abbrev-ref", "@{u}"); err != nil {
		// no upstream yet: pull if the remote branch exists, then push -u
		branch, _ := r.Git("rev-parse", "--abbrev-ref", "HEAD")
		if _, err := r.Git("ls-remote", "--exit-code", "--heads", "origin", branch); err == nil {
			if err := r.pullRebase(prefer, "origin", branch); err != nil {
				return strings.Join(log, "\n"), err
			}
		}
		if _, err := r.Git("push", "-q", "-u", "origin", "HEAD"); err != nil {
			return strings.Join(log, "\n"), err
		}
		return strings.Join(append(log, "pushed (upstream set)"), "\n"), nil
	}
	before, _ := r.Git("rev-parse", "HEAD")
	if err := r.pullRebase(prefer); err != nil {
		return strings.Join(log, "\n"), err
	}
	if after, _ := r.Git("rev-parse", "HEAD"); after != before {
		changes, _ := r.Git("diff", "--stat", before, after)
		log = append(log, "pulled:\n"+changes)
	}
	ahead, _ := r.Git("rev-list", "--count", "@{u}..HEAD")
	if ahead != "0" {
		if _, err := r.Git("push", "-q"); err != nil {
			return strings.Join(log, "\n"), err
		}
		log = append(log, "pushed "+ahead+" commit(s)")
	}
	if len(log) == 0 {
		log = append(log, "already up to date")
	}
	return strings.Join(log, "\n"), nil
}
