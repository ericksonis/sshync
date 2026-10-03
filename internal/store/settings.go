package store

import (
	"errors"
	"os"

	"github.com/BurntSushi/toml"
)

// Settings live in the repo (sshync.toml) so both machines share defaults.
type Settings struct {
	Defaults struct {
		User           string `toml:"user"`
		Identity       string `toml:"identity"` // file name in keys/, or a path
		IdentitiesOnly bool   `toml:"identities_only"`
		ForwardAgent   bool   `toml:"forward_agent"`
	} `toml:"defaults"`
	Sync struct {
		AutoCommit bool `toml:"autocommit"`
		AutoPush   bool `toml:"autopush"`
	} `toml:"sync"`
}

func DefaultSettings() Settings {
	var s Settings
	s.Defaults.IdentitiesOnly = true
	s.Sync.AutoCommit = true
	return s
}

const settingsHeader = `# sshync settings (shared between machines).
# [defaults] pre-fill "sshync add"; identity is a file name in keys/ or a path.
`

func (p Paths) LoadSettings() (Settings, error) {
	s := DefaultSettings()
	_, err := toml.DecodeFile(p.Settings, &s)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	return s, err
}

func (p Paths) SaveSettings(s Settings) error {
	f, err := os.CreateTemp(p.Repo, ".sshync-*")
	if err != nil {
		return err
	}
	f.WriteString(settingsHeader)
	if err := toml.NewEncoder(f).Encode(s); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	f.Close()
	return os.Rename(f.Name(), p.Settings)
}
