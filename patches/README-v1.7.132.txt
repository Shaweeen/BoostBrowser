v1.7.132 remaining wiring (Shaweeen)

Windows pack machine — apply ALL parts in order, then pack v1.7.132:

  git fetch origin release/1.7.132-ext-auth-proxy
  git checkout release/1.7.132-ext-auth-proxy
  git apply patches/split/v1.7.132-remaining-part1a-syncer.patch
  git apply patches/split/v1.7.132-remaining-part1b-start.patch
  git apply patches/split/v1.7.132-remaining-part1c-navigate.patch
  git apply patches/split/v1.7.132-remaining-part1d-watch-script.patch
  git apply patches/split/v1.7.132-remaining-part2.patch
  python3 scripts/test_packaging_scripts.py

Sensitive: does not rewrite Cookies / Preferences / LES / wallets /
user-assigned --load-extension. Helper source stays on disk.
Start no longer unpacks helper or attaches store debugger.
