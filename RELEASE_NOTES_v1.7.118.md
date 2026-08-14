# BrowserStudio v1.7.118

## UUID data integrity during updates

- New environments always use one identity: the generated profile UUID maps to `data/<the same UUID>`.
- Normal startup and the existing online-update path validate that mapping without moving, copying, deleting or rewriting browser data.
- If a database row is missing but `data/<UUID>` still contains Chromium state, the client attaches that environment in place under the same UUID.
- If a stale database path is missing while the matching UUID directory exists, the client realigns the row to `data/<UUID>`.
- If both the old path and the UUID path contain data, the update is cancelled and the conflict is reported instead of guessing which Cookies, extension or wallet state is authoritative.
- The v1.7.117 same-ID extension inventory and signed Chrome Web Store recovery remain active, so durable extension/account data is reconnected to its original extension ID under Chrome for Testing 148.

## Absolute synchronized zoom

- `Ctrl + mouse wheel` in sync mode no longer replays only a relative delta to followers.
- After the master window applies its zoom, followers are measured and stepped to the same absolute render/CSS scale.
- Rapid wheel input is debounced into one final alignment, preventing followers with different starting zoom levels from remaining out of sync.

## Verification

- UUID creation, missing-row attachment, stale-path alignment and ambiguous-path rejection tests.
- Browser application/extension state preservation regression test.
- Absolute zoom direction and tolerance test.
- Full Go test suite, Windows amd64 compilation/build, frontend production build and packaging tests.
