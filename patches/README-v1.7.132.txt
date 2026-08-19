v1.7.132 remaining wiring (Shaweeen)

Git identity: Shaweeen <79692938+Shaweeen@users.noreply.github.com>

Windows pack machine — apply ALL remaining parts in order, then pack:

  git fetch origin release/1.7.132-ext-auth-proxy
  git checkout release/1.7.132-ext-auth-proxy
  git apply patches/split/v1.7.132-remaining-part1a-syncer.patch
  git apply patches/split/v1.7.132-remaining-part1b-start.patch
  git apply patches/split/v1.7.132-remaining-part1c-navigate.patch
  git apply patches/split/v1.7.132-remaining-part2.patch
  python3 scripts/test_packaging_scripts.py

Sensitive data: patches do not rewrite Cookies, Preferences, Local
Extension Settings, wallets, or user-assigned --load-extension.
Helper source stays on disk; Start no longer attaches it.
