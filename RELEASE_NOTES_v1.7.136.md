# BrowserStudio v1.7.136

## Chrome-owned extension lifecycle

- Normal startup trusts the settled extension marker and no longer rereads Preferences/LES or performs client-side extension repair.
- Post-update maintenance no longer downloads, rewrites, or rebinds user extensions. Chrome/profile remains the owner of installed extensions and wallet state.
- Explicit extension assignment remains available when the user intentionally requests it.
- Cookies, login state, wallet vaults, Local Extension Settings, and profile data are not deleted or reset.
