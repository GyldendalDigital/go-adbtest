# go-adbtest Work Breakdown

This document is the authoritative execution plan for the initial local build.
It supplements [ROADMAP.md](../ROADMAP.md) with task boundaries, dependencies,
acceptance criteria, and recovery status.

## Execution Rules for the Initial Build

- Work directly on `master`, as requested for the initial development pass.
- Keep all new commits local until the user explicitly asks for a push.
- Use one focused commit per package or review task.
- Write tests before or alongside implementation.
- Update [session.md](session.md) whenever status, decisions, validation results,
  or the next action changes.
- Normal branch-per-task and push-after-task instructions are intentionally
  deferred for this initial build. They apply again after the local build is
  reviewed and the user decides how to publish it.

## Task Status

| ID | Work package | Depends on | Status | Local commit |
| --- | --- | --- | --- | --- |
| M0 | Repository, docs, and CI foundation | — | Complete, pushed | `0906160`, `f307813` |
| M1 | `adb/` command wrapper | M0 | Complete | `fc886a9` |
| M2 | `emulator/` lifecycle | M1 | Complete | `679c724` |
| M3 | `ui/` native interaction | M1 | Complete | `b3f64c7` |
| M4 | `cdp/` WebView interaction | M1 | Complete | `41abba5` |
| M5 | `permissions/` dialog handling | M3 | Complete | `af43f5a` |
| M5A | Critical `ui/` stabilization found by recovery audit | M3 | Complete | `63dc142` |
| M5B | Critical `cdp/` connection/error stabilization found by recovery audit | M4 | Complete | `28473f5` |
| M5C | Critical `adb/` and `emulator/` lifecycle stabilization found by recovery audit | M1–M2 | Complete | `d6601d3` |
| M6 | Root `adbtest` device/testkit API | M1–M5C | Complete | `4497d83` |
| M7 | Examples and public documentation | M6 | Complete | `67731ad`, `e1f5665`, `229ea36`, `99e6c1d` |
| M8 | Cross-package council review and release-quality validation | M1–M7 | Complete locally | `31b3564`, `820400d`, `9019bb5` |

## Task Specifications and Acceptance Criteria

### M5 — Runtime permissions

Files: `permissions/permissions.go`, `permissions/permissions_test.go`, status
documents as needed.

Acceptance criteria:

- Prefer full access, then API 30+ foreground/one-time buttons, then API 23–29
  `Allow`/`ALLOW`; expose Android 14 limited-media selection separately.
- Deny API 30+ (`Don't allow`) and API 23–29 (`Deny`/`DENY`) buttons.
- Restrict matches to known Android permission-controller packages so app text
  cannot be tapped accidentally.
- Drain delayed sequential permission dialogs with one overall timeout and a
  maximum of five grants.
- Return useful internal errors and expose the planned `testing.TB` helpers
  without panics.
- Cover selection, timeout, controller detection, tapping, and multi-dialog
  behavior with deterministic unit tests.
- `go test ./...` and `go vet ./...` pass before the local commit.

### M6 — Root device/testkit API

Files: `testkit.go`, `testkit_test.go`, and any directly affected package APIs
required for clean composition.

Acceptance criteria:

- The root package is `adbtest`, matching the module import and existing
  `doc.go` (the implementation spec's `package testkit` snippet is stale).
- Validate configuration before starting external processes.
- Apply documented duration, GPU, and CDP-port defaults without hiding Go bool
  zero-value semantics.
- Boot an AVD, install the APK, determine the application package when needed,
  launch it, and connect UI/CDP helpers in dependency order.
- Select the default or an explicit secondary Android process and discover its
  actual WebView DevTools socket without replacing an existing ADB forward.
- Clean up every resource already acquired when a later setup step fails.
- Implement idempotent teardown plus force-stop, launch, and restart behavior.
- Exercise orchestration and command construction through deterministic test
  seams; no Android SDK is required for unit tests.

### M5A–M5C — Recovered prerequisite stabilization

The recovery council found correctness problems that would make the composed
root API unreliable. Fix them in focused package commits before M6:

- `ui/`: isolate XML from the normal trailing `uiautomator` status line, use
  the documented file/pull fallback if fast parsing fails, and reject malformed
  element bounds rather than tapping `(0,0)`.
- `cdp/`: propagate protocol and disconnect errors, make concurrent close safe,
  bound HTTP discovery by the caller's retry timeout, validate HTTP status, and
  test visible-selector semantics.
- `adb/` and `emulator/`: reconcile the public `Devices` API, send emulator
  shutdown through `adb emu kill` rather than `adb shell`, detect early process
  exit, reap failed processes, and keep setup within one boot-time budget.

Each package fix must add regression tests and pass its race test and vet before
its local commit. Broader non-blocking refinements remain in M8.

### M7 — Examples and documentation

Files: `examples/`, `README.md`, `ROADMAP.md`, implementation spec corrections,
and package documentation.

Acceptance criteria:

- Provide a build-tagged, compile-checked example of the intended `TestMain`
  and per-test flow.
- Provide a consumer GitHub Actions template that provisions an emulator,
  builds/installs an APK, and runs integration tests.
- Ensure every exported identifier has accurate godoc.
- Make README examples compile against the final API and clearly distinguish
  library unit tests from consumer Android integration tests.
- Reconcile discovered specification contradictions with the implemented API.

### M8 — Council review and validation

Files: any package where a correctness issue is found, plus status documents.
Fixes are committed separately from the original feature commits.

Acceptance criteria:

- Review as Go API, Android platform, CDP, test infrastructure, and code-quality
  specialists.
- Verify process cleanup, goroutine/channel closure, command timeouts, error
  propagation, shell input handling, and public API consistency.
- Run unit tests (including race detector), vet, formatting checks, build, module
  tidiness, and integration-example compilation.
- Record any checks that require an Android SDK/emulator and could not be run.
- Leave a clean local working tree and do not push.

M8 passed locally on 2026-07-31: the full race suite, vet, golangci-lint, native
build, Windows cross-build, module tidy and verification, formatting/diff
checks, the SDK-free tagged example, and govulncheck all succeeded.

## External Follow-up

Run the copyable consumer example against a real debuggable WebView APK on the
intended Android API matrix. This is not a local M8 failure: the current host
does not have an Android SDK, emulator, `adb`, `aapt`, or an application fixture.
Publishing the local commits is intentionally deferred until the user requests
it.
