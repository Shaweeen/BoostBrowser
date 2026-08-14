# BrowserStudio v1.7.116

## Windows upgrade data-path fix

- Reuses the unique existing BrowserStudio data root recorded by an earlier client installation instead of assuming a drive letter or fixed directory.
- Recognizes BrowserStudio Manager, BrowserStudio, and BoostBrowser installation identities while leaving ambiguous multiple-root cases for explicit user selection.
- Preserves browser profiles, Cookies, extension storage, wallet data, kernels, proxies, configuration, and activation state in place.
- Prevents profile, proxy, and browser-core list calls from crashing while application startup is still incomplete.

## Verification

- Packaging script tests
- Full Go test suite and Go vet
- Frontend production build
- Windows amd64 cross-build
- NSIS installer syntax compilation
