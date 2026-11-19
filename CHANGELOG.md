# Changelog

## 0.2.0 — unreleased

**Store format 2.** Run `ccsync migrate` once after upgrading every machine
(ccsync 0.1.x can't read a migrated store).

### Fixed
- The GUI and a CLI command running at once could corrupt a store's clone;
  every operation now holds a per-store lock across processes.
- The GUI token was in the URL handed to the browser (readable by other
  local users in the process list); the URL now carries a one-time code the
  page trades for the token. The GUI also checks the Host header and sends
  CSP and no-referrer headers.
- `pull --force` backed up only the transcript; the companion directory is
  kept too. Pull no longer replaces a session Claude Code is writing.
- One unreadable store copy aborted a whole push; it is now reported (and
  replaced with `--force`).
- Forgetting a store whose clone couldn't be removed left it registered.
- Long project paths sharing a 200-character prefix could be installed into
  the wrong project's folder.
- A push the remote rejected (network error, or another machine pushing
  first) left an unpushed commit in the local clone; the next push then
  reported the session "already up to date" and it never reached the
  remote. The clone is now reset to the remote before every operation, and
  losing a push race is retried automatically.
- A failed or interrupted run could leave the clone dirty or mid-rebase,
  making every later command fail until fixed by hand.
- Subagent transcripts, persisted tool results and other files in the
  session's companion directory (`<id>/`) were not synced.
- `cwd` fields pointing into project subdirectories kept the old path after
  a pull; references to persisted tool results ("Full output saved to: …")
  now point at the new location too.
- The session title was the first `/rename` instead of the latest.
- Project paths over 200 characters were installed into the wrong folder;
  symlinked project paths are now resolved like Claude Code does.
- `pull --force` overwrote a local session without a backup, and writes
  were not atomic. Identical re-pulls are now a no-op; a replaced local copy
  is kept as `<id>.jsonl.bak-<time>`.
- A global `commit.gpgsign` setting broke store commits.
- If the first push of `init` was rejected (e.g. bad credentials), re-running
  `init` reported success with nothing on the remote and the store stayed
  unusable.
- A pull into a subdirectory of the original project rewrote cwd twice.
- `init` now accepts a repo created with a README (the store is added on
  top) and a destination path that doesn't exist yet is resolved through
  symlinked parents, like Claude Code does.
- Git line-ending conversion (`core.autocrlf`) can no longer touch store blobs.
- The GUI could run two git operations on the store at once.

### Changed
- Redesigned GUI start screens: stores to unlock and store kinds are
  picked from cards, errors show next to the form, and passphrases can be
  revealed. Loading states while a project or the store loads; long store
  names no longer break the header.
- Unlocking derives the passphrase key once; sessions are encrypted to a
  random store key. Listing 100 sessions no longer takes ~1 minute.
- Sessions are gzipped before encryption (a real 38 MB project stored as
  8 MB).
- One unreadable store entry no longer hides all the others.
- Bundles may carry entries a newer ccsync adds; older versions skip them.
- Git runs with `LC_ALL=C`, and never prompts for credentials from the GUI.
- Building needs Go 1.27.4 or later (`go install` fetches it automatically).
- Module path is `github.com/vinh0809it/ccsync` (`go install` works).
- The GUI API accepts only the intended HTTP method per endpoint.

### Added
- Sync status in the GUI: every session on this machine shows whether it
  is synced, newer here, newer in the store, diverged or not pushed yet,
  with a count of the ones to act on; store sessions not on this machine
  are marked too. Pushes record a fingerprint of the
  session's entries, so in-sync copies are recognized without decrypting.
- Progress for push and pull: the GUI shows each step (packing, encrypting,
  syncing, uploading, pulling) with a session count; the CLI keeps a status
  line on the terminal.
- Delete several store sessions at once in the GUI.
- Pull many sessions at once: `pull --all` / `pull <id>...` and **Pull
  selected** in the GUI, each to its original project path moved by folder
  mappings (`--map FROM=TO`; the GUI proposes one when the home directory
  differs and previews every destination). Failures are listed without
  stopping the rest; `--dry-run` shows the plan.
- Synced-folder stores (`init --folder`, or "Synced folder" in the GUI): keep
  the store in a folder that Dropbox, iCloud Drive, Google Drive, OneDrive or
  Syncthing syncs — no git needed.
- `init --github NAME` (GUI: "New private GitHub repo") creates a private
  repo through the GitHub CLI; git then uses the `gh` login. Public GitHub
  repos are refused unless `--allow-public`.
- Several stores per machine, each with its own name, repo and passphrase:
  `init --name`, `store list|use|forget`, `--store NAME` (or
  `$CCSYNC_STORE`); the GUI picks, adds and switches stores. `store forget`
  (GUI: "Forgot the passphrase?") removes a store from this machine only, the
  way out of a lost passphrase. Machines set up by 0.1 keep working as the
  store "default".
- Push and pull are fast-forward only: by comparing message ids (and renames), an outdated
  copy can no longer overwrite a newer one, and a session continued on two
  machines is reported instead of silently losing one side. `--force`
  overrides (pull keeps a backup; push leaves the old copy in git history).
- Encrypted bundles are split into 45 MB files, so sessions of any size fit
  git hosts' file limits (GitHub refuses files over 100 MB). Bundling and
  encryption stream, keeping memory near 3× the session size.
- A pulled session keeps its original modification time.
- Active sessions met during a push are skipped and reported instead of
  aborting the whole push.
- `ccsync push --all-projects`, and "All projects" in the GUI. Large pushes
  are split into commits of at most ~64 MB so memory stays bounded.
- Native-window apps for Windows, macOS (.app) and Linux (with an installer),
  built by the release workflow. `ccsync` with no arguments opens the GUI, so
  every download works on double-click.
- `ccsync rm` (and a Delete button in the GUI) to remove sessions from the store.
- `push --include-active`; `--force` now only ever means "overwrite".
- `ccsync migrate`, `ccsync version`.
- CI (gofmt, vet, race tests, govulncheck) and tagged releases with checksums.

## 0.1.0

First release.
