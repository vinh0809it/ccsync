# Changelog

## 0.2.0 — unreleased

**Store format 2.** Run `ccsync migrate` once after upgrading every machine
(ccsync 0.1.x can't read a migrated store).

### Fixed
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
- The GUI could run two git operations on the store at once.

### Changed
- Unlocking derives the passphrase key once; sessions are encrypted to a
  random store key. Listing 100 sessions no longer takes ~1 minute.
- Sessions are gzipped before encryption (a real 38 MB project stored as
  8 MB).
- One unreadable store entry no longer hides all the others.
- Module path is `github.com/vinhvh0809/ccsync` (`go install` works).

### Added
- `ccsync migrate`, `ccsync version`.
- CI (gofmt, vet, race tests, govulncheck) and tagged releases with checksums.

## 0.1.0

First release.
