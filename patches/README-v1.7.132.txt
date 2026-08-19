v1.7.132 remaining wiring (Shaweeen)

Git identity: Shaweeen <79692938+Shaweeen@users.noreply.github.com>
Do not use galaylm/chanx for this repo.

This branch already has the small 1.7.132 files. MCP cannot upload 70KB+
sources, so apply one remaining patch on Windows before packing.

  git fetch origin release/1.7.132-ext-auth-proxy
  git checkout release/1.7.132-ext-auth-proxy
  git apply --check patches/v1.7.132-remaining.patch
  git apply patches/v1.7.132-remaining.patch
  python3 scripts/test_packaging_scripts.py
  then pack/publish v1.7.132 as usual

If the combined patch is missing, apply the split parts in order:

  git apply patches/split/v1.7.132-remaining-part1.patch
  git apply patches/split/v1.7.132-remaining-part2.patch

Only if you already applied patches/v1.7.131-remaining.patch on these
same large files, use:

  git apply patches/v1.7.132-from-applied-131.patch

The remaining patch does not rewrite Cookies, Preferences, Local
Extension Settings, wallets, or user-assigned --load-extension paths.
Helper source (cloak_web_store_helper.go) stays on disk; Start no longer
attaches it.
