# BrowserStudio v1.7.17 release audit

Audit date: 2026-07-20

## Architecture reviewed

| Boundary | Main responsibility | Result |
| --- | --- | --- |
| Wails host and lifecycle | Window startup/shutdown, watchdog, update handoff | Pass after cross-platform lifecycle stub fix |
| Browser manager | Profile isolation, kernels, runtime recovery, launch arguments | Pass |
| Window synchronization | Single master ownership, Windows hooks, layout/scroll forwarding | Pass; Windows-only implementation is build isolated |
| Proxy subsystem | Parser, bridges, pool assignment, latency and subscription state | Pass; full slow protocol suite executed |
| Extension subsystem | Global import, profile binding, CRX download and startup cleanup | Pass; remote downloads use public-address SSRF controls |
| Wallet import | Rabby/Jupiter/MetaMask one-way import | Pass within the documented extension trust boundary |
| Local launch API | Loopback binding, generated API key, origin and Host checks | Pass |
| Updater | GitHub release lookup, download, hash, replacement and rollback | Hardened in this release |
| Installer | Runtime verification, activation, scoped process cleanup | Script validation pass; final NSIS build must run on Windows |
| Frontend | React routes, Wails API use, local metadata and production bundle | Pass |

## Verification evidence

- `go test ./... -count=1`: pass.
- `go test ./backend/test/proxy -count=1 -v`: pass in 339.583 seconds.
- `go vet ./...`: pass.
- Windows/amd64 `go test -c` cross-compilation for every Go package: pass.
- Windows/amd64 Wails production executable: pass.
- `npm run build`: pass.
- `npm audit` for production and development dependencies: 0 findings across
  367 dependencies.
- `govulncheck ./...`: 0 reachable vulnerabilities after dependency upgrades.
- Packaging script unit tests: 16 passed.
- Secret-pattern scan: no private key, GitHub token, cloud access key, or
  embedded user credential found in tracked source.

## Security controls confirmed

- Launch API binds to loopback, creates a random 256-bit API key when missing,
  uses constant-time comparison, validates Host and Origin, and limits headers
  and request bodies.
- Subscription and extension downloads reject localhost, private, link-local,
  multicast, CGNAT, DNS-rebinding targets, unsafe redirects, and inherited
  system proxies.
- Wallet mnemonics are not returned to the frontend, database, logs, or export
  functions. Secret sessions are one-use and expire from backend memory.
- The updater accepts only the stable GitHub release asset path, verifies a
  64-character hexadecimal SHA-256 digest, checks the `MZ` PE header, and only
  applies the path verified in the current process session.
- Installer cleanup selects processes by executable path under
  `<install>\chrome` or `<install>\bin`, plus the exact installed main/updater
  executables.

## Residual risks and release conditions

- Executables and NSIS installers are not Authenticode code-signed. SHA-256
  protects integrity after publication but does not replace publisher signing.
- Offline installer activation is a local verifier and is not equivalent to a
  server-signed entitlement system.
- Proxy credentials are part of local proxy configuration and are not protected
  against malware or another process running as the same Windows user.
- A wallet extension necessarily receives and stores key material. BrowserStudio
  cannot prove the behaviour of a closed-source extension or protect against a
  compromised browser, extension, administrator, debugger, or keylogger.
- The private full installer contains locally supplied third-party browser and
  proxy binaries. It must be built on the trusted Windows packaging PC and must
  not be uploaded to a public GitHub release.
- BrowserStudio does not implement CAPTCHA solving or anti-bot bypass.

Release decision: the lightweight v1.7.17 update assets are ready after their
published GitHub hashes are verified. The private full NSIS installer is ready
to build on the Windows packaging PC using the pinned one-command workflow.
