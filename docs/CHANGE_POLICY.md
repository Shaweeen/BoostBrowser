# Change and code-slimming policy

BrowserStudio uses replacement-oriented maintenance: a bug fix or redesigned
feature replaces obsolete logic instead of accumulating another patch layer.

## Required workflow

### 1. Establish the authoritative behavior

Write down the current requirement and the lifecycle boundary it belongs to.
Examples include environment startup, explicit extension distribution, sync
Start/Stop/Refresh, proxy verification, popup confinement, and data recovery.

### 2. Audit the same module before editing

Inspect:

- the implementation being changed;
- all direct callers and platform variants;
- earlier compatibility branches and fallbacks;
- timers, goroutines, hooks, listeners, polling and cached state;
- duplicated parsing, validation, persistence and window enumeration;
- tests that preserve retired behavior rather than current behavior.

Do not add a second worker, state owner, scan path or fallback until the existing
owner has been evaluated.

### 3. Replace, then remove

Keep a single authoritative implementation. Remove the superseded path when:

- it has no active caller;
- its replacement is covered by a focused test; or
- the old behavior is intentionally retired by the current requirement.

Compatibility code may remain only when it protects existing user data or a
documented supported input/version. Label its boundary and test it.

### 4. Record reversible deletion

Every material deletion must add an entry to `docs/DELETION_LEDGER.md` with:

- exact removed path or symbol;
- last known revision;
- reason;
- maintained replacement;
- completed verification;
- smallest safe recovery instruction.

The ledger is a recovery index, not permission to restore an obsolete subsystem
wholesale.

### 5. Recovery after an incorrect deletion

If a later release loses a required function:

1. reproduce and identify the exact missing behavior;
2. locate its ledger entry and inspect the recorded revision;
3. restore only the minimal code needed;
4. adapt it to the current state and lifecycle owner;
5. add a regression test for the loss;
6. rerun the same-module redundancy audit;
7. remove any temporary compatibility bridge;
8. run the complete verification matrix again.

### 6. Verification matrix

At minimum:

- formatter and `git diff --check`;
- focused regression tests for the changed lifecycle;
- `go test ./...`;
- Windows x64 test compilation, build, and `go vet -unsafeptr=false`;
- frontend TypeScript and production build;
- packaging-script tests;
- release code-health guard.

Any skipped check must be reported explicitly; it must not be described as
passing.

## Prohibited patterns

- retaining old and new implementations "just in case";
- permanent polling added to repair an action-driven workflow;
- multiple owners mutating the same sync/window/runtime state;
- background startup scans that duplicate explicit user actions;
- silent destructive cleanup of profile, cookie, extension or wallet data;
- fixed unauthenticated localhost control bridges;
- broad release rollback to recover one removed function;
- claiming code slimming based only on file size without caller and behavior
  verification.
