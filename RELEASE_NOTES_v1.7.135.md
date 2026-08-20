# BrowserStudio v1.7.135

## Extension retention hardening

- A launch-integrity marker is now revalidated against Chrome Preferences and durable extension runtime data on every normal start.
- If a user-downloaded or explicitly assigned extension lost its Chrome registration, the launcher keeps `--load-extension` and repairs the normal load path instead of silently hiding the extension.
- Cookies, login state, wallet vaults, Local Extension Settings, and profile data are not deleted or reset.
