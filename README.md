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

Download a release binary, or build from source (Go 1.23+, no C compiler needed):

```bash
git clone git@github.com:osean-man/pinner.git
cd pinner
go build -o pinner .
sudo mv pinner /usr/local/bin/
```

To cross-compile, set `GOOS` and `GOARCH`, e.g. `GOOS=windows GOARCH=amd64 go build -o pinner.exe .`.
The sync server builds the same way: `go build -o pinner-server ./cmd/pinner-server`.

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
go test ./...          # unit, sync and end-to-end tests (the e2e tests build the binaries)
go test -short ./...   # skip the test that builds the original release to check migration
```

The interactive end-to-end tests drive the real menu inside tmux and are skipped if tmux isn't installed.
CI runs on Linux and macOS and cross-compiles every release target. Pushing a `v*` tag publishes a release
with GoReleaser.
