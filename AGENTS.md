# BrowserStudio change rules

These rules apply to every code change in this repository.

## Source of truth and release workflow (mandatory)

1. **GitHub is the only source of truth for code.** Develop and commit so that
   every finished change is on GitHub (push the branch/tag the user uses).
2. **Windows is the only official pack/publish machine.** After code is on
   GitHub, the user updates Windows with `git pull` / checkout, then builds and
   uploads the Release there (`scripts/publish_windows_github_release.ps1` or
   project bat/ps1).
3. **Do not treat Mac (or any non-Windows host) as the formal binary publisher.**
   Do not upload `boost-browser.exe` / Setup installers to GitHub Releases from
   Mac unless the user explicitly asks for an exception. Notes-only releases or
   tags from the dev machine are fine when requested.
4. **Version bumps and release notes** live in the repo on GitHub first; Windows
   only packages what it pulls.
5. Prefer small, reviewable commits on the agreed branch (e.g.
   `release/1.7.95-from-1.7.83` or whatever the user names next), not local-only
   thrash with many productVersion jumps without push.

User summary (Chinese): 以后在 GitHub 上改好并推送；Windows 电脑 `git` 拉代码后再打包发布。

---

1. Do not stack a new workaround beside an old implementation. Before changing
   a module, search that module and its direct callers for earlier fixes,
   duplicate state, duplicate workers, compatibility branches, polling loops,
   and superseded platform implementations.
2. Treat the current product requirement as the authoritative behavior. Remove
   code that implements an obsolete behavior after proving that it has no
   required caller or after the replacement is covered by tests.
3. Keep one owner for each lifecycle, state machine, timer, hook, listener,
   window scan, persistence write, and network operation. Do not leave old and
   new owners active in parallel.
4. Do not delete user data compatibility, cookies, login state, extension or
   wallet storage, profile identity, activation state, or rollback data as
   "cleanup."
5. Record every material deletion in `docs/DELETION_LEDGER.md`, including the
   last known revision, reason, replacement, verification, and precise recovery
   path.
6. If a removed behavior must be restored, recover only the smallest relevant
   part from the ledger revision, adapt it to the current architecture, add a
   regression test, and rerun the same redundancy scan. Never revert a whole
   release over current user data or unrelated fixes.
7. Before handing off a change, run formatting, repository diff checks, relevant
   focused tests, full Go tests, frontend production build, Windows cross-build
   and vet, and packaging tests when those toolchains are available.
8. Keep untracked and unrelated user files untouched.

The detailed review workflow is in `docs/CHANGE_POLICY.md`.
