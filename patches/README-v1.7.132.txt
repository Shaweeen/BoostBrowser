v1.7.132 remaining wiring (Shaweeen)

Git identity: Shaweeen <79692938+Shaweeen@users.noreply.github.com>
Do not use galaylm/chanx for this repo.

This branch already has the small 1.7.132 files. MCP cannot upload 70KB+
sources, so the large files still look like v1.7.130 until you apply a patch.

Large files in the remaining patch:
  backend/app_instance.go          no start-path helper / store debugger
  backend/app_input_syncer.go      OAuth never Runtime.evaluate'd
  docs/DELETION_LEDGER.md          CLEAN-112 + CLEAN-113
  frontend/package-lock.json       1.7.130 -> 1.7.132
  frontend/.../WindowSyncPage.tsx  Authorize-once hint

Windows pack machine (normal path, from v1.7.130 / this branch as pushed):

  git fetch origin release/1.7.132-ext-auth-proxy
  git checkout release/1.7.132-ext-auth-proxy
  git apply --check patches/v1.7.132-remaining.patch
  git apply patches/v1.7.132-remaining.patch
  python3 scripts/test_packaging_scripts.py
  then pack/publish v1.7.132 as usual

Only if you already applied patches/v1.7.131-remaining.patch on these
same large files, use the smaller follow-up instead:

  git apply --check patches/v1.7.132-from-applied-131.patch
  git apply patches/v1.7.132-from-applied-131.patch

Sensitive data: the patch does not rewrite Cookies, Preferences, Local
Extension Settings, wallets, or user-assigned --load-extension paths.
Helper source (cloak_web_store_helper.go) stays on disk; Start no longer
attaches it.
