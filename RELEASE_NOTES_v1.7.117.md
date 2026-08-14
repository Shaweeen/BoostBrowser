# BrowserStudio v1.7.117

## Chrome 148 extension recovery

- Scans the bundled `chrome` directory during client startup and prefers the verified Chrome for Testing 148 kernel as the default.
- Records only each environment's extension IDs in the persistent `data/profile-extension-inventory.json`; wallet data, Cookies, IndexedDB values and page content are never read or copied.
- Before an online EXE update, refreshes that ID inventory and cancels the update if the record cannot be saved safely.
- Detects the exact failure observed after v1.7.115 upgrades: `Preferences`/`Secure Preferences` and `Default/Extensions` lost while `Local Extension Settings` or extension IndexedDB still exists.
- Downloads the signed Chrome Web Store CRX for the original ID, verifies the CRX public key, stores the package under the persistent `data/extensions/imported` directory and loads it with Chrome for Testing 148.
- Reconnects the recovered extension to the existing same-ID wallet/account storage without rewriting Chromium-owned `Preferences`, `Secure Preferences`, `Local State`, Cookies or extension databases.
- Rejects keyless profile packages for automatic recovery because unpacked Chromium would derive a new ID from the filesystem path and fail to reconnect the original account data.

## Verification

- Focused extension identity, orphan detection, Secure Preferences, stable-ID recovery and Chrome 148 selection tests.
- Full Go tests and vet.
- Frontend production build.
- Windows amd64 test compilation/build/vet.
- Packaging-script and release code-health tests.
