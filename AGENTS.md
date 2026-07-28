# BrowserStudio change rules

These rules apply to every code change in this repository.

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
