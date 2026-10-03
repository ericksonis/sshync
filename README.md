# sshync

Manage `~/.ssh/config` one host at a time and keep it in sync between machines through a private git repo.

Built for agent setups such as Bitwarden, 1Password or Secretive. These agents hold many keys, so every host needs
`IdentityFile <key>.pub` + `IdentitiesOnly yes`. Without that, the agent offers every key and the server stops after `MaxAuthTries`.

## Install

```powershell
scoop bucket add ericksonis https://github.com/ericksonis/scoop-bucket
scoop install sshync
# or: go install github.com/ericksonis/sshync/cmd/sshync@latest
```

`git` and OpenSSH must be on `PATH`.

## How it works

`sshync init` backs up `~/.ssh/config` to `config.bak-<timestamp>`, splits it into one file per host, and replaces it with a small bootstrap:

```
~/.ssh/config                      # "# Managed by sshync" + Include lines (absolute paths)
~/.ssh/sshync/local.d/*.conf       # hosts for this machine only (not synced)
~/.ssh/sshync/local-defaults.conf  # Host */Match blocks for this machine only
~/.ssh/sshync/repo/                # private git repo, synced
    hosts.d/<alias>.conf           # one Host block per file → merges rarely conflict
    defaults.conf                  # shared Host * block
    keys/*.pub                     # public keys (never private keys)
    sshync.toml                    # defaults for `sshync add`
```

For each option, ssh uses the first value it reads. So local hosts override shared ones, and `Host *` blocks come last.
Comments and formatting inside each file are preserved; only lines you edit are rewritten.

Identity files are written as `~/.ssh/sshync/repo/keys/<name>.pub`. Win32-OpenSSH expands `~` in `IdentityFile`
to `%USERPROFILE%`, so the same file works on every machine.
`Include` is different: there `~` expands to `%HOME%`, which Git Bash and MSYS may set to another directory.
That is why the bootstrap uses absolute paths.

## Setup

First machine:

```powershell
sshync init                 # imports existing hosts into the repo
git -C $HOME\.ssh\sshync\repo remote add origin git@github.com:you/ssh-hosts.git   # a PRIVATE repo
sshync sync
```

Other machines:

```powershell
sshync init --repo git@github.com:you/ssh-hosts.git
```

This imports the machine's own config into the repo. Hosts that already match the repo are skipped. For hosts that differ, sshync shows a diff and asks which side to keep.
Non-interactive runs need `--prefer repo|mine`.
Hosts that should not be synced: `sshync import --to local <file>` or `sshync mv <alias> --to local`.

## Everyday use

```powershell
sshync add web1 -H web1.example.com -u deploy -k id_ed25519_work   # prompts for anything missing unless -y
sshync list                         # or: sshync list prod / sshync ls '*.lan' / --json
sshync show web1 [-e]               # -e also prints `ssh -G web1`
sshync set web1 Port 2222
sshync set web1 SendEnv LANG --add  # repeatable keys
sshync unset web1 Port
sshync toggle web1 ForwardAgent     # yes <-> no; --on / --off
sshync fwd add web1 -L 8080:localhost:80 -D 1080
sshync fwd rm web1 -L 8080:localhost:80
sshync rename web1 web-01 ; sshync mv web-01 --to local ; sshync rm web-01
sshync edit web1                    # $SSHYNC_EDITOR / $VISUAL / $EDITOR / notepad
sshync edit --defaults
sshync keys add work_laptop -       # paste a public key from Bitwarden: Get-Clipboard | sshync keys add work_laptop -
sshync keys list
sshync sync                         # commit, pull --rebase, push
sshync doctor
```

By default each edit to the repo is committed automatically. Push happens on `sshync sync`, or after every edit if you set `autopush = true` under `[sync]` in `sshync.toml`.

If the same host changed on both machines, `sync` aborts the rebase and leaves your files untouched, so ssh keeps working. Re-run with `--prefer mine` or `--prefer remote` to pick a side.

## Development

```
go test ./...
SSHYNC_REAL_CONFIG=$HOME/.ssh/config go test ./internal/sshconf -run Real   # byte-exact round trip of your real config
```

Tests set `SSHYNC_SSH_DIR` to a temp directory, so they never touch your real `~/.ssh`.

## Releasing

Push a tag such as `v0.1.0`. The `release` workflow runs the tests, then goreleaser builds the binaries, creates the GitHub release,
and commits `sshync.json` to [ericksonis/scoop-bucket](https://github.com/ericksonis/scoop-bucket).
That last step needs the `SCOOP_BUCKET_TOKEN` repository secret: a token that can push to the bucket repo.

## License

MIT, see [LICENSE](LICENSE).
