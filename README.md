# ccsync

Sync [Claude Code](https://claude.com/claude-code) sessions between machines
through an **end-to-end-encrypted git repository**. Even on a public repo,
nobody without the passphrase can read anything — transcripts, titles,
project names and paths are all encrypted (the only visible things are
random session UUIDs and commit timestamps).

Push a session on your laptop, pull it on your desktop, `claude --resume` it —
even when the project lives at a different path or on another OS.

## Install

Every machine needs `git` with access to your store repo. Then pick one from
the [releases](https://github.com/vinh-vh/ccsync/releases) (check downloads
against `SHA256SUMS`):

| You want | Download | Then |
|---|---|---|
| **An app** (native window, double-click) | Windows: `…-windows-amd64-desktop.exe`<br>macOS: `…-macos-arm64.zip`<br>Linux: `…-linux-amd64-desktop.tar.gz` | Windows: run it (Windows 10/11 include the WebView2 it needs).<br>macOS: unzip, move `ccsync.app` to Applications; the first time, right-click → Open (the app is not notarized).<br>Linux: unpack, run `./install.sh` — it adds ccsync to your app menu (needs `webkit2gtk-4.1`). |
| **A single file**, any OS | `ccsync-<version>-<os>-<arch>` | Run it: with no arguments it opens the GUI in your browser; it is also the full CLI. |
| **From source** | `go install github.com/vinh-vh/ccsync@latest` | |

The app and the browser GUI do the same things: set up, unlock, push, pull,
delete.

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
ccsync push --all                # push every session of the current project
ccsync push --all-projects       # push every session of every project
ccsync ls                        # list what's in the store
ccsync rm <id>                   # delete a session from the store

# on the other machine:
ccsync pull <id> --to ~/dev/myproject
cd ~/dev/myproject && claude --resume <id>
```

`--all` and `--all-projects` skip sessions active in the last 30 seconds
(Claude Code may still be writing them). `--project <dir>` targets a project
other than the current directory. The
passphrase is prompted, or read from `$CCSYNC_PASSPHRASE` for scripting.
`CCSYNC_CONFIG_DIR` / `CCSYNC_STORE_DIR` override the default locations
(`~/.config/ccsync`, `~/.local/share/ccsync/store`); `CLAUDE_CONFIG_DIR` is
honoured like Claude Code does.

### GUI

```sh
ccsync                     # same as `ccsync gui`
ccsync gui --port 8931 --no-open
```

Pick a project (or **All projects**), tick sessions, push; pull or delete
from the store list.

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
them.

## Push, pull and conflicts

ccsync decides like git does with fast-forwards. Claude Code gives every
message a unique id and only ever appends (also when compacting), so of two
copies of a session, the newer one contains all the messages of the older
one — whatever path it was relocated to. ccsync compares those message ids,
and counts a rename (`/rename`, or a title set in claude-sessions) as an entry
too:

| Your copy vs. the other | `push` | `pull` |
|---|---|---|
| same messages (paths may differ) | nothing to do | nothing to do |
| older (the other has more) | **rejected** — pull instead | updates your copy |
| newer (you have more) | replaces the store copy | **rejected** — push instead |
| diverged (both continued) | **rejected** | **rejected** |
| can't be compared (no message ids found) | **rejected** | **rejected** |

Rejected cases need `--force` (in the GUI, a confirmation):

- `push --force` replaces the store copy with yours. The replaced version is
  not lost — it stays in the store repo's git history.
- `pull --force` replaces your copy and keeps it as
  `<id>.jsonl.bak-<unix-time>` next to the session.

So a forgotten, outdated copy can never silently overwrite newer work, and a
session continued on two machines is always flagged. What ccsync does *not*
do is merge two diverged copies into one: a conversation is a single thread,
so you keep one side (the other is in the backup or the git history).

Several machines pushing at the same moment is handled: the push that loses
the race re-syncs and retries automatically. A pulled session keeps its
original modification time, so session lists (and claude-sessions) sort it
where it was, not as "just now".

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

## Passphrases and multiple stores

- **Forgotten passphrase:** there is no recovery — that is what end-to-end
  encryption means. Your sessions are still on the machines that have them:
  create a new store repo, `ccsync init` it with a new passphrase on each
  machine, and push again.
- **Several stores** (e.g. work and personal, each with its own passphrase):
  a machine's setup lives in two directories, so point them elsewhere per
  store:

  ```sh
  export CCSYNC_CONFIG_DIR=~/.config/ccsync-work CCSYNC_STORE_DIR=~/.local/share/ccsync-work
  ccsync init git@github.com:you/work-sessions.git
  ```

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
- The git history keeps every pushed version, including sessions removed
  with `ccsync rm`. To reclaim space (or truly erase), start a new store repo
  and push again.
- Big sessions are fine for the store (blobs are split into 45 MB files, under
  every git host's limit) but each push of one adds its full compressed size
  to the repo's history, and pushing or pulling it needs about 3–4× its size
  in memory (a 512 MB transcript: ~1.7 GB).
- For project paths over 200 characters Claude Code adds a hash to the folder
  name that ccsync can't reproduce: start one Claude Code session in the
  destination first, then pull.

## Store layout (format 2)

```
ccsync.json                      plaintext marker {"format": 2}
check.age                        known value, passphrase-encrypted (compatibility)
key.age                          store key, passphrase-encrypted
sessions/<uuid>/data.age         encrypted bundle (transcript + companion dir);
                                 past 45 MB continued in data.age.1, data.age.2, ...
sessions/<uuid>/meta.age         encrypted metadata (title, original cwd, host, ...)
```

The local clone is a cache: every command resets it to the remote first, so a
failed or interrupted run can't leave it stuck. A push that loses a race to
another machine is retried automatically.

## Building

```sh
go build .                    # this platform
./build/dist.sh               # browser-mode binaries for all platforms -> dist/
./build/desktop.sh            # the native-window app for the OS you run it on
```

Browser-mode binaries are pure Go (no CGO) and cross-compile. The
native-window app (`-tags desktop`) uses the OS web view through CGO, so each
OS builds its own; Linux needs `libwebkit2gtk-4.1-dev` to build. Both scripts
stamp `$VERSION` (default: the current git tag). Pushing a `v*` tag runs the
release workflow, which builds everything on Linux, macOS and Windows and
publishes a GitHub release.

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
- [x] Fast-forward-only push and pull, with retry when machines race
- [x] `ccsync rm` to delete sessions from the store
- [x] Native apps for Windows, macOS and Linux
- [ ] OS keychain caching of the passphrase
- [ ] Signed/notarized macOS and Windows builds

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md); report security issues as described in
[SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
