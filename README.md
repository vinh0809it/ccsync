# ccsync

Sync [Claude Code](https://claude.com/claude-code) sessions between machines
through an **end-to-end-encrypted git repository**. Even on a public repo,
nobody without the passphrase can read anything — transcripts, titles,
project names and paths are all encrypted (the only visible things are
random session UUIDs and commit timestamps).

Push a session on your laptop, pull it on your desktop, `claude --resume` it —
even when the project lives at a different path or on another OS.

## Install

Download a binary from the [releases](https://github.com/vinhvh0809/ccsync/releases)
(Linux, macOS, Windows; check it against `SHA256SUMS`), or:

```sh
go install github.com/vinhvh0809/ccsync@latest
```

The only runtime dependency is `git`.

## Setup

On every machine, with the same repo and the same passphrase:

```sh
ccsync init git@github.com:you/my-session-store.git
```

The repo should be empty the first time (create it private on GitHub/GitLab/…).
Each machine needs its own git access to it — ccsync uses your existing SSH
key or credential helper and never sees or stores credentials.

## Usage

```sh
ccsync sessions                  # list local sessions of the current project
ccsync push <id> [<id>...]       # push selected sessions (id prefixes work)
ccsync push --all                # push all (skips sessions active in the last 30 s)
ccsync ls                        # list what's in the store

# on the other machine:
ccsync pull <id> --to ~/dev/myproject
cd ~/dev/myproject && claude --resume <id>
```

`--project <dir>` targets a project other than the current directory. The
passphrase is prompted, or read from `$CCSYNC_PASSPHRASE` for scripting.
`CCSYNC_CONFIG_DIR` / `CCSYNC_STORE_DIR` override the default locations
(`~/.config/ccsync`, `~/.local/share/ccsync/store`); `CLAUDE_CONFIG_DIR` is
honoured like Claude Code does.

### GUI

```sh
ccsync gui                 # opens the browser UI (setup, unlock, push, pull)
ccsync gui --port 8931 --no-open
```

The GUI is served by the same binary on `127.0.0.1` only. Every API request
requires a random per-run token embedded in the URL it prints, so other local
users can't drive it. The unlocked key is held in memory for the run and
never written to disk.

## What gets synced, and how a pull relocates it

A Claude Code session is a transcript plus a companion directory:

```
~/.claude/projects/<path-slug>/<session-id>.jsonl   transcript
~/.claude/projects/<path-slug>/<session-id>/        subagents/, tool-results/, ...
```

`push` bundles both (gzipped tar), encrypts the bundle and its metadata, and
commits them to the store. Unchanged sessions are skipped (content hash), so
re-pushing is cheap and doesn't grow the repo.

`pull --to <dir>` installs the session under the slug Claude Code uses for
`<dir>` (symlinks resolved, like Claude Code) and rewrites exactly two kinds
of path:

- the structural `"cwd"` fields — the project root and its subdirectories;
- references to the session's own companion directory, e.g. the
  "Full output saved to: …/tool-results/…" lines, which end in the session
  UUID and so can't match anything else.

Other paths inside message content are left alone; `--resume` doesn't need
them. Pulling an identical session again is a no-op. If a *different* local
copy exists, pull refuses; `--force` replaces it and keeps the old one as
`<id>.jsonl.bak-<unix-time>`.

Not synced: `/rewind` checkpoints (`~/.claude/file-history/<session-id>/`,
backups of files the session edited, tied to the original machine's paths)
and other machine-local state outside `projects/`. Resuming and continuing
the conversation doesn't need them.

## Security model

- **Encryption:** [age](https://age-encryption.org). A random X25519 store
  key encrypts every session and metadata blob; the key itself is stored in
  the repo encrypted with your passphrase (scrypt). The passphrase is the only
  secret — use a long one. Anyone who has it (or a machine that cached an
  unlocked GUI) can read and write the whole store.
- **What a repo reader sees:** session UUIDs, blob sizes, commit times, and
  that you use ccsync. Commit messages carry no titles or hostnames.
- **Changing the passphrase does not revoke it:** old versions of the key
  file stay in git history. If a passphrase leaks, start a new store repo.
- **Peers are untrusted input:** session ids, bundle paths and metadata from
  the store are validated before they touch the filesystem (no traversal, no
  symlinks, regular files only).
- **Git credentials** are never read or stored; ccsync shells out to `git`.

## Upgrading from 0.1.x

0.2 uses store format 2 (the store key, companion directories, compression).

1. Upgrade ccsync on **every** machine first — 0.1.x can't read a migrated
   store.
2. On one machine run `ccsync migrate` (one-time; ~1.5 s per session).
3. Re-push from the machine where each session lives to add its subagents
   and tool results (sessions migrated from 0.1.x only contain the
   transcript).

If a 0.1.x machine pushes after the migration, `ccsync ls` warns about the
entries it can't read; running `ccsync migrate` again converts them.

## Caveats

- The `.jsonl` transcript format is **internal to Claude Code** and can change
  between versions. ccsync treats it as an opaque blob apart from the
  rewrites above. Keep Claude Code versions roughly in sync across machines;
  `ccsync ls` doesn't show it, but the stored metadata records which version
  wrote each session.
- Never resume the *same* session on two machines at once and push both —
  the last push wins.
- The git history keeps every pushed version. To reclaim space, start a new
  store repo and push again.
- For project paths over 200 characters Claude Code adds a hash to the folder
  name that ccsync can't reproduce: start one Claude Code session in the
  destination first, then pull.

## Store layout (format 2)

```
ccsync.json                      plaintext marker {"format": 2}
check.age                        known value, passphrase-encrypted (compatibility)
key.age                          store key, passphrase-encrypted
sessions/<uuid>/data.age         encrypted bundle (transcript + companion dir)
sessions/<uuid>/meta.age         encrypted metadata (title, original cwd, host, ...)
```

The local clone is a cache: every command resets it to the remote first, so a
failed or interrupted run can't leave it stuck. A push that loses a race to
another machine is retried automatically.

## Building

```sh
go build .                    # this platform
./build/dist.sh               # browser-mode binaries for all platforms -> dist/
./build/dist.sh desktop       # also the Linux native-window binary
```

Browser-mode binaries are pure Go (no CGO). The Linux native-window build
(`-tags desktop`) uses WebKit and needs `webkit2gtk`;
`./packaging/linux/install.sh` installs it with an app-menu launcher under
`~/.local`. Native Windows/macOS windows need a build on those OSes and are
not set up yet. `VERSION=1.2.3 ./build/dist.sh` stamps a version (by default
the current git tag); pushing a `v*` tag builds and publishes a GitHub
release.

The app icon source is `assets/icon.svg`; regenerate the rasters with:

```sh
magick -background none assets/icon.svg -resize 256x256 assets/icon-256.png
magick -background none assets/icon.svg -define icon:auto-resize=16,32,48,64,128,256 assets/icon.ico
```

## Tests

```sh
go test ./...
```

They run against real bare git remotes in temp dirs: crypto round-trips and
wrong-passphrase rejection; transcript parsing, cwd and companion-path
rewriting, bundle validation and install semantics; two-machine round-trips,
push failure recovery and push races, dirty-clone recovery, migration from
format 1, and a "nothing readable in the repo" leak check; plus the full GUI
API flow over HTTP.

## Roadmap

- [x] GUI (local web UI served by the binary)
- [x] Conflict-aware push (retry after another machine pushes)
- [ ] `ccsync rm` to delete sessions from the store
- [ ] OS keychain caching of the passphrase
- [ ] Native windows on Windows/macOS (CI build matrix)

## License

MIT
