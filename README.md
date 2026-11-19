# ccsync

Move [Claude Code](https://claude.com/claude-code) sessions between your
machines. Push a session on your laptop, pull it on your desktop,
`claude --resume` it — even when the project lives at a different path or on
another OS.

Claude Code keeps each CLI session on the machine that ran it; there is no
built-in way to continue one elsewhere. ccsync encrypts sessions on your
machine and shares them through a store you own: a **private GitHub repo**
(any git repo works) or a **folder that Dropbox, iCloud Drive, Google Drive,
OneDrive or Syncthing already syncs** — no git needed. Nobody without your
passphrase can read anything in it: transcripts, titles, project names and
paths are all encrypted.

## Install

Pick one from the [releases](https://github.com/vinh0809it/ccsync/releases)
(check downloads against `SHA256SUMS`):

| You want | Download | Then |
|---|---|---|
| **An app** (its own window) | Windows: `…-windows-amd64-desktop.exe`<br>macOS: `…-macos-arm64.zip`<br>Linux: `…-linux-amd64-desktop.tar.gz` | Windows: run it (Windows 10/11 include the WebView2 it needs).<br>macOS: unzip, move `ccsync.app` to Applications; the first time, right-click → Open (the app is not notarized yet).<br>Linux: unpack, run `./install.sh` — it adds ccsync to your app menu (needs `webkit2gtk-4.1`). |
| **A single file**, any OS | `ccsync-<version>-<os>-<arch>` | Run it: with no arguments it opens the GUI in your browser; it is also the full CLI. |
| **From source** | `go install github.com/vinh0809it/ccsync@latest` | |

A git store also needs `git`; creating a GitHub repo for you needs the
[GitHub CLI](https://cli.github.com) (`gh auth login`).

## Set up a store

Do this on every machine, with the same store and the same passphrase. In
the GUI, the first screen asks the same three questions.

```sh
ccsync init --github claude-sessions          # create a private GitHub repo and use it
ccsync init https://github.com/you/repo.git   # or use an existing git repo
ccsync init --folder ~/Dropbox/claude-sessions  # or a synced folder, no git
```

- **GitHub through the GitHub CLI** is the easiest git option: the repo is
  created private, and git uses your `gh` login (`gh auth setup-git`, which
  `gh auth login` offers) — no SSH keys. ccsync never sees or stores
  credentials.
- **Store repos must be private.** ccsync refuses a public GitHub repo
  (`--allow-public` overrides): your sessions are encrypted, but a public
  repo still shows when and how much you sync.
- **A synced folder** must be empty the first time (or be the store another
  machine created). Let your sync app finish before pulling on the other
  machine.

The store gets a name on this machine, from the repo or folder name unless
you pass `--name`; see [several stores](#several-stores-and-a-forgotten-passphrase).

## Usage

```sh
ccsync sessions                  # list local sessions of the current project
ccsync push <id> [<id>...]       # push selected sessions (id prefixes work)
ccsync push --all                # push every session of the current project
ccsync push --all-projects       # push every session of every project
ccsync ls                        # list what's in the store
ccsync rm <id>                   # delete a session from the store

# on the other machine:
ccsync pull <id> --to ~/dev/myproject        # one session, to a project you choose
cd ~/dev/myproject && claude --resume <id>
ccsync pull --all                             # every session, each to its original project path
ccsync pull --all --map /home/alice/dev=/Users/alice/code   # ...where projects live elsewhere here
ccsync pull --all --dry-run --map ...         # only show where each would go
```

`pull --all` (or several ids) keeps each session's project path, moved by
`--map FROM=TO` rules (repeatable; the longest match wins; it works across
OSes, e.g. `C:\Users\alice\dev=/Users/alice/code`). If the projects came from
a home directory that doesn't exist here, ccsync stops and suggests the
`--map` to use. Sessions it can't install — continued here, open in Claude
Code — are listed; the rest still go through.

Sessions written to in the last 30 seconds are skipped — Claude Code may
still be writing them (`--include-active` pushes them anyway).
`--project <dir>` targets a project other than the current directory. The
passphrase is prompted, or read from `$CCSYNC_PASSPHRASE` for scripting.
`CCSYNC_CONFIG_DIR` / `CCSYNC_DATA_DIR` override where the list of stores and
their local data live (`~/.config/ccsync`, `~/.local/share/ccsync`);
`CLAUDE_CONFIG_DIR` is honoured like Claude Code does.

### GUI

```sh
ccsync                     # same as `ccsync gui`
ccsync gui --port 8931 --no-open
```

Moving every session to another machine takes two clicks on each side:

1. **Machine A:** pick **All projects**, tick **Select all**, **Push selected**.
2. **Machine B:** set up the same store (for a GitHub store: **Existing git
   repo** with the URL shown in A's header — not "New private GitHub repo"),
   same passphrase. In **In the store**, tick **Select all**, **Pull
   selected**. The dialog shows where each session will go; if your projects
   live elsewhere on B, add a folder mapping (ccsync proposes one when the
   home directory differs) and press **Pull**. Mappings are remembered per
   store in that browser.

Single sessions can be pulled (to any folder) or deleted from their row.
The GUI and the CLI can run at the same time: they take turns on a store.

## What gets synced, and how a pull relocates it

A Claude Code session is a transcript plus a companion directory:

```
~/.claude/projects/<path-slug>/<session-id>.jsonl   transcript
~/.claude/projects/<path-slug>/<session-id>/        subagents/, tool-results/, ...
```

`push` bundles both (gzipped tar) and encrypts the bundle and its metadata
into the store. Unchanged sessions are skipped, so re-pushing is cheap.

`pull --to <dir>` installs the session under the folder Claude Code uses for
`<dir>` (symlinks resolved, like Claude Code) and rewrites exactly two kinds
of path:

- the structural `"cwd"` fields — the project root and its subdirectories;
- references to the session's own companion directory, e.g. the
  "Full output saved to: …/tool-results/…" lines, which end in the session
  UUID and so can't match anything else.

Other paths inside message content are left alone; `--resume` doesn't need
them. A pulled session keeps its original modification time, so session
lists (and [claude-sessions](https://github.com/vinh0809it/claude-sessions))
sort it where it was.

Not synced: `/rewind` checkpoints (`~/.claude/file-history/<session-id>/`,
backups of files the session edited, tied to the original machine's paths)
and other machine-local state outside `projects/`. Resuming and continuing
the conversation doesn't need them.

## Push, pull and conflicts

ccsync decides like git does with fast-forwards. Claude Code gives every
message a unique id and only ever appends (also when compacting), so of two
copies of a session, the newer one contains all the messages of the older
one — whatever path it was relocated to. ccsync compares those message ids,
and counts a rename (`/rename`, or a title set in claude-sessions) as an
entry too:

| Your copy vs. the other | `push` | `pull` |
|---|---|---|
| same messages (paths may differ) | nothing to do | nothing to do |
| older (the other has more) | **rejected** — pull instead | updates your copy |
| newer (you have more) | replaces the store copy | **rejected** — push instead |
| diverged (both continued) | **rejected** | **rejected** |
| can't be compared (unreadable) | **rejected** | **rejected** |

Rejected cases need `--force` (in the GUI, a confirmation):

- `push --force` replaces the store copy with yours. In a git store the
  replaced version stays in the repo's history; a synced folder keeps no
  history of its own (most sync services do).
- `pull --force` replaces your copy and keeps it — transcript and companion
  directory — next to it with a `.bak-<unix-time>` suffix.

A pull also refuses to replace a session that Claude Code is writing to
right now (modified in the last 30 seconds); close it first.

So an outdated copy can never silently overwrite newer work, and a session
continued on two machines is always flagged. What ccsync does *not* do is
merge two diverged copies: a conversation is a single thread, so you keep
one side (the other is in the backup or the history).

## Several stores, and a forgotten passphrase

A machine can be set up for several stores — say `work` and `personal`, each
with its own passphrase. Each has a name on that machine; one is current.

```sh
ccsync init --github work-sessions --name work
ccsync init --folder ~/Dropbox/claude --name personal
ccsync store list               # * marks the current store
ccsync store use work           # switch the current store
ccsync --store personal ls      # or pick one for a single command ($CCSYNC_STORE works too)
ccsync store forget work        # remove a store from this machine
```

In the GUI, the start screen lets you pick a store or add another, and
**Switch store** in the header goes back to it.

**Forgotten passphrase:** there is no recovery — that is what end-to-end
encryption means. Forget the store on this machine (`ccsync store forget`,
or **Forgot the passphrase?** in the GUI), set up a new store, and push
again. Forgetting removes only this machine's registration (and a git
store's local clone): your Claude Code sessions, the repo or folder, and
other machines are untouched. Sessions that exist only in the old store stay
unreadable.

## Security model

- **Encryption:** [age](https://age-encryption.org). A random X25519 store
  key encrypts every session and metadata blob; the key itself is stored in
  the store encrypted with your passphrase (scrypt). The passphrase is the
  only secret — use a long one. Anyone who has it can read and write the
  whole store.
- **What someone with access to the store sees:** session UUIDs, blob
  sizes, and when you sync. Commit messages carry no titles or hostnames.
  Keep stores private anyway.
- **Changing the passphrase does not revoke it:** old versions of the key
  file stay in git history (or your sync service's). If a passphrase leaks,
  start a new store.
- **The store is untrusted input:** session ids, bundle paths, sizes and
  metadata are validated before they touch the filesystem (no traversal, no
  symlinks, regular files only, size limits).
- **The GUI** listens on `127.0.0.1` only, answers only to localhost host
  names, and requires a random per-run token on every API call. The token
  never appears in a URL or command line: the address ccsync opens carries a
  one-time code that the page trades for it. The unlocked key is held in
  memory and never written to disk.
- **Credentials** are never read or stored: ccsync runs your `git` and `gh`.

## Upgrading from 0.1.x

0.2 uses store format 2 (the store key, companion directories, compression).

1. Upgrade ccsync on **every** machine first — 0.1.x can't read a migrated
   store.
2. On one machine run `ccsync migrate` (one-time; ~1.5 s per session).
3. Re-push from the machine where each session lives to add its subagents
   and tool results (sessions migrated from 0.1.x only contain the
   transcript).

A machine set up by 0.1 keeps working: its store shows up as `default`. If
a 0.1.x machine pushes after the migration, `ccsync ls` warns about the
entries it can't read; running `ccsync migrate` again converts them.

## Caveats

- The `.jsonl` transcript format is **internal to Claude Code** and can change
  between versions. ccsync treats it as an opaque blob apart from the
  rewrites and comparisons above. Keep Claude Code versions roughly in sync
  across machines.
- A git store's history keeps every pushed version, including sessions
  removed with `ccsync rm`. To reclaim space (or truly erase), start a new
  store and push again.
- Big sessions work (blobs are split into 45 MB files, under every git
  host's limit), but each push of one adds its compressed size to a git
  store's history, and pushing or pulling it needs about 3–4× its size in
  memory (a 512 MB transcript: ~1.7 GB).
- A synced folder is only as consistent as its sync service: let it finish
  before pulling on another machine, and don't push the same session from
  two machines at once.
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

The same files live in a git repo or a synced folder. A git store's local
clone is a cache: every command resets it to the remote first, so a failed
or interrupted run can't leave it stuck, and a push that loses a race to
another machine is retried automatically. Every file is written atomically.

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

They run against real bare git remotes and folders in temp dirs, simulating
several machines: two-machine round trips through git and through a synced
folder, fast-forward rules, push failures and races, dirty-clone recovery,
process locking, migration from format 1, path relocation, bundle
validation, a "nothing readable in the store" leak check, and the GUI API
over HTTP.

## Roadmap

- [x] GUI, native apps for Windows, macOS and Linux
- [x] Fast-forward-only push and pull, with retry when machines race
- [x] Several stores per machine; git repos and synced folders
- [ ] Push automatically when a Claude Code session ends (a `SessionEnd` hook)
- [ ] `ccsync history` / `restore` for versions kept in a git store
- [ ] OS keychain caching of the passphrase
- [ ] Signed/notarized macOS and Windows builds; Homebrew and Scoop

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md); report security issues as described in
[SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
