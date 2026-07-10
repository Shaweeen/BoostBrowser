# Boost Browser

A Wails/Go-based fingerprint browser with Chromium kernel and built-in proxy tooling.

## Features

- Multi-profile management with isolated fingerprints
- Chromium kernel management
- Built-in xray / sing-box proxy tooling
- Multi-window operation sync (master/follower)

## Build from source

Prerequisites: Go 1.22+, Node 20+, Wails CLI v2.

For the Windows self-use package with CloakBrowser Chromium, see `WINDOWS_BUILD.md`.

Quick Windows flow:

```powershell
powershell -ExecutionPolicy Bypass -File scripts\install_cloakbrowser_kernel.ps1
cd frontend
npm ci
npm run build
cd ..
powershell -ExecutionPolicy Bypass -File scripts\build_release.ps1
powershell -ExecutionPolicy Bypass -File scripts\build_installer.ps1
```

## Project layout

- `main.go` / `backend/` — Go backend (Wails)
- `frontend/src/` — React + TypeScript UI
- `scripts/` — PowerShell build / release scripts
- `build/windows/` — Windows installer assets
