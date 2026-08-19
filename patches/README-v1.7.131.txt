v1.7.131 remaining wiring
=========================

The branch already contains:
- backend/navigation_guards.go
- backend/navigation_guards_test.go
- backend/webstore_compat_watch.go (store-URL discover, no auto-attach)
- RELEASE_NOTES_v1.7.131.md
- wails.json / frontend/package.json bumped to 1.7.131

Apply the last five file edits on a Windows checkout of this branch:

    git apply --whitespace=nowarn patches/v1.7.131-remaining.patch

This patches:
- backend/app_instance.go       (do not inject Web Store compat on about:blank)
- backend/app_input_syncer.go   (do not URL-sync or replay OAuth)
- docs/DELETION_LEDGER.md       (CLEAN-112)
- frontend/package-lock.json    (1.7.131)
- scripts/test_packaging_scripts.py

Then pack/publish as usual.
