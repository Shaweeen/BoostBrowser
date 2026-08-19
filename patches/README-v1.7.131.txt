v1.7.131 remaining wiring (Shaweeen)

Git identity for this work is Shaweeen (79692938+Shaweeen@users.noreply.github.com).
The galaylm/chanx git identity is retired and must not be used for this repo.

Already on this branch (applied):
  wails.json / frontend/package.json -> 1.7.131
  backend/navigation_guards.go + _test.go
  backend/webstore_compat_watch.go
  RELEASE_NOTES_v1.7.131.md

Still needs `git apply` on the Windows pack machine (MCP cannot upload 70KB+ files):
  patches/v1.7.131-remaining.patch

  backend/app_instance.go          store-URL-only initial-tab inject
  backend/app_input_syncer.go      skip OAuth URL-sync + click/key replay
  docs/DELETION_LEDGER.md          CLEAN-112
  frontend/package-lock.json       1.7.130 -> 1.7.131
  scripts/test_packaging_scripts.py store-URL / no-auto-attach guard

Windows:
  git fetch origin release/1.7.131-oauth-ext-isolation
  git checkout release/1.7.131-oauth-ext-isolation
  git apply --check patches/v1.7.131-remaining.patch
  git apply patches/v1.7.131-remaining.patch
  python3 scripts/test_packaging_scripts.py
  then pack/publish v1.7.131 as usual
