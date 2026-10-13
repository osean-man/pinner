# Pinner

A small tool to pin commands you use often and pick them from a searchable menu. It's like shell history,
but only holds what you chose to keep. Pins live in a local SQLite database, and you can sync them between
your devices end-to-end encrypted, so the sync server can't read or change them.

## Features

- **Searchable menu**: run, copy (`-c`) or print (`-p`) a pin. Search matches commands, labels and tags.
- **Labels and tags**: `-n "tail prod logs" -t k8s,prod`. Filter the menu with `pinner -t k8s`.
- **Placeholders**: `ssh <<user>>@<<host>> -p <<port:22>>` asks for values when you run the pin.
- **Smart ordering**: the pins you use most, and used most recently, come first.
- **Sensitive pins**: `--sensitive` masks a command in menus and lists (good for commands with tokens).
- **Shell integration**: Alt+p puts a pin on your prompt so you can edit it before running it, and `pin-last` pins the command you just ran.
- **Editor support**: `--edit` writes or edits multi-line commands in `$EDITOR`.
- **Import and export** as JSON.
- **Encrypted sync** between devices through a server you host (see [Sync](#sync)).
- **Pure Go, no cgo**: one static binary for Linux, macOS and Windows.

## Usage

```text
pinner [flags]                  open the menu and run the selected pin
pinner add [command...]         pin a command
pinner list                     list pins
pinner update [id]              change a pin
pinner remove [id]              remove a pin (alias: rm)
pinner export / import          back up or restore pins as JSON
pinner init <bash|zsh|fish>     print shell integration
pinner sync ...                 set up and run encrypted sync

Menu flags:
  -c, --copy      copy the command to your clipboard instead of running it
  -p, --print     print the command to stdout instead of running it
  -t, --tag       only show pins with this tag
      --confirm   show the final command and ask before running it (or set PINNER_CONFIRM=1)
```

### Examples

```bash
# Pick a pin and run it. Pinner exits with the command's exit status.
pinner

# Copy, or print for use in scripts
pinner -c
eval "$(pinner -p)"

# Add pins: as arguments, piped in, from $EDITOR, or from your history
pinner add ls -la
echo "ls -la" | pinner add
pinner add --edit
pinner add --last

# Labels, tags and flags go before the command (use -- when the command starts with a dash)
pinner add -n "worker logs" -t k8s,prod -- kubectl logs -l app=worker -f -n platform

# Placeholders: <<name>> or <<name:default>>
pinner add -n tunnel -- 'ssh -i ~/.ssh/id_ed25519 <<user>>@<<host>> -L <<port:5601>>:localhost:<<port>>'

# Sensitive pins are masked as "curl ••••••" in the menu and in `pinner list`
pinner add --sensitive -n "search api" -- curl -H "X-API-KEY: ..." https://api.example.com
pinner list --show-sensitive

# Change or remove a specific pin (IDs are shown by `pinner list`)
pinner update 4 --label "db shell" --tag db
pinner update 4 --edit
pinner rm 4 -y

# Back up and restore
pinner export -o pins.json
pinner import pins.json
```

Placeholders use `<<name>>` because `{{...}}` shows up in real commands (`docker --format`, kubectl go-templates).
A command with no `<<name>>` placeholders is never changed. Values are inserted exactly as typed, so quote them
yourself if they contain spaces or shell characters.

### Shell integration

Add one of these to your shell's startup file:

```bash
eval "$(pinner init bash)"   # ~/.bashrc
eval "$(pinner init zsh)"    # ~/.zshrc
pinner init fish | source    # ~/.config/fish/config.fish
```

- **Alt+p** opens the menu and inserts the chosen pin at your cursor without running it. You can edit it first,
  and it lands in your shell history as a normal command. This also avoids a problem with running pins directly:
  the output of `pinner | tee file` included pinner's menu.
- **`pin-last [add flags]`** pins the command you just ran, e.g. `pin-last -n "rebuild" -t dev`.

`pinner add --last` reads your history file instead. Bash only writes that file when a shell exits, so
`pin-last` is more reliable.

The bash integration is covered by automated tests. The zsh and fish scripts are syntax-checked only when
those shells are installed.

### Running pins

Selecting a pin runs it with `$SHELL -c`. Pins are commands you saved yourself, but **anything that can
change your pins can run commands as you**. Pinner protects that in three ways:

- The database is only readable by you: mode 0600 in a 0700 directory.
- Synced pins are authenticated, so the server can't inject or alter them.
- `--confirm` (or `PINNER_CONFIRM=1`) shows the final command before running it.

## Sync

Sync works like [hiSHtory](https://github.com/ddworken/hishtory): your first device creates a random secret
key. Every other key is derived from it, and pins are encrypted on your device before they're uploaded. The server only
stores ciphertext under opaque IDs. It can't read your pins, and it can't forge or alter one, because tampered
records are rejected and never applied.

```bash
# On your first device
pinner sync init --server https://pins.example.com
# prints your secret: PINR-XXXX-XXXX-...

# On each other device
pinner sync join PINR-XXXX-XXXX-... --server https://pins.example.com

pinner sync          # sync now (also happens automatically, see below)
pinner sync status   # server, pending changes, local-only pins
pinner sync key      # show the secret again
pinner sync leave    # stop syncing on this device (add --purge to delete your data from the server)
```

- Opening the menu pulls changes, waiting at most 1.5s. Adding, updating or removing a pin pushes it, waiting at most 3s.
  If the server can't be reached you see a one-line warning and pinner keeps working offline. Changes sync the next time
  it can connect.
- `pinner add --local ...` or `pinner update <id> --local` keeps a pin on this device only. Making a synced pin
  local removes it from your other devices.
- Usage counts are per device and aren't synced.
- When the same pin is edited on two devices, the later edit wins.
- **Keep the secret safe.** Anyone with it can read and change your pins. It is stored in
  `~/.config/pinner/sync.json` (mode 0600). If you lose it and all your devices, the synced copy can't be
  recovered.
- The server rejects plain `http://` URLs except for localhost, because the access token would travel unencrypted. You
  can override that with `PINNER_ALLOW_INSECURE=1` if you understand the risk. Set `PINNER_SYNC=off` to disable
  automatic syncing.

The design and threat model are in [docs/sync-design.md](docs/sync-design.md). To run your own server, see
[docs/self-hosting.md](docs/self-hosting.md).

## Installation

### From a release (recommended)

Releases have prebuilt binaries for Linux (amd64, arm64, armv7, 386, riscv64), macOS (Intel and Apple
Silicon), Windows (amd64, arm64) and FreeBSD. The install script picks the right one, checks its SHA-256
checksum against the release's `checksums.txt` and installs it to `/usr/local/bin` (using sudo if needed):

```bash
curl -fsSL https://raw.githubusercontent.com/osean-man/pinner/master/install.sh | sh
```

Options: `PINNER_VERSION=v1.0.0` to install a specific release, `BIN_DIR=~/.local/bin` to install elsewhere, and
`PINNER_VERIFY_ATTESTATION=1` to also check the build provenance with the GitHub CLI.

**Updating** works the same way: run the install script again. Check what you're running with `pinner --version`;
it prints the release version and the exact commit it was built from.

### Verifying a download yourself

Every release includes `checksums.txt` (SHA-256 for every archive), an SPDX SBOM per archive, and a signed
[build provenance attestation](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations)
proving the archive was built by this repository's release workflow from the tagged commit:

```bash
sha256sum --ignore-missing -c checksums.txt
gh attestation verify pinner_1.0.0_linux_amd64.tar.gz --repo osean-man/pinner
```

Builds are reproducible: the same tag built with the same Go toolchain gives byte-identical binaries.

### From source

Go 1.26+ is required; there's no C compiler dependency. If your Go is older than the `toolchain` line in `go.mod`,
Go downloads that version automatically.

```bash
git clone https://github.com/osean-man/pinner.git
cd pinner
make install            # builds and copies to /usr/local/bin (sudo make install if you don't own it)
make install BINDIR=~/.local/bin
```

`go build` on its own only writes `./pinner` in the current directory; it doesn't install anything. Cross-compile
with `GOOS`/`GOARCH`, e.g. `GOOS=windows GOARCH=amd64 go build -o pinner.exe .`. The sync server builds with
`make server`.

### Data location

Everything lives in `~/.config/pinner/` (override with `PINNER_CONFIG_DIR`):

| File | Contents |
|---|---|
| `commands.db` | your pins (SQLite) |
| `commands.db.bak` | copy of your database taken before an upgrade changed its format |
| `sync.json` | sync server URL and secret (only if sync is set up) |

Upgrading from an earlier version migrates your existing database automatically, after making the backup above.

## Development

```bash
make test              # go test -race ./...  (unit, sync and end-to-end tests)
make lint              # gofmt, go vet, govulncheck
make snapshot          # build every release artifact into dist/ without publishing (needs Docker)
go test -short ./...   # skip the test that builds the original release to check migration
```

The interactive end-to-end tests drive the real menu inside tmux and are skipped if tmux isn't installed.

### CI/CD

- **CI** (`.github/workflows/ci.yml`) runs on every push and pull request. It runs the tests on Linux (amd64 and
  arm64), macOS and Windows, checks formatting, runs `go vet`, govulncheck and shellcheck, and does a full
  GoReleaser snapshot build plus a Docker build, so a broken release config fails CI before anything is tagged.
- **Releases** (`.github/workflows/release.yml`) run when a `v*` tag is pushed. The full CI suite runs first. Then
  GoReleaser publishes the archives, checksums and SBOMs, the archives get a build provenance attestation, and a
  multi-arch (amd64/arm64) `ghcr.io/osean-man/pinner-server` image is pushed with its own SBOM and attestation.
- **Pinning:** every GitHub Action is pinned to a full commit SHA, Docker base images are pinned by digest, the Go
  toolchain is pinned in `go.mod`, and GoReleaser and govulncheck run at fixed versions. Dependabot opens weekly
  grouped PRs to update the actions, Go modules and images.

To cut a release:

```bash
git tag -a v1.2.3 -m "v1.2.3" && git push origin v1.2.3
```
