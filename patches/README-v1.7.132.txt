v1.7.132 remaining wiring (Shaweeen)

Windows — if part1a + part1b already applied, do NOT reset --hard.
Fetch the repaired part1c1, then continue (skip part1c2, it is now inside part1c1):

  git fetch origin release/1.7.132-ext-auth-proxy
  git checkout origin/release/1.7.132-ext-auth-proxy -- patches/split/v1.7.132-remaining-part1c1-navigate.patch
  git apply --ignore-whitespace patches/split/v1.7.132-remaining-part1c1-navigate.patch
  git apply --ignore-whitespace patches/split/v1.7.132-remaining-part1d-watch-script.patch
  git apply --ignore-whitespace patches/split/v1.7.132-remaining-part2.patch
  python scripts/test_packaging_scripts.py

Do not apply part1c2. Cookies / Preferences / LES / wallets / user --load-extension are not rewritten.
