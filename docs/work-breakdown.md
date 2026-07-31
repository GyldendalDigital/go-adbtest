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
| M9 | Host Android smoke and lightweight emulator profile | M8 | Complete locally; live run deliberately bounded | `b418420`, `a2522ef`, `e503bb0`, `3999a1c`, `6efa95b` |

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
- Start an existing configured AVD, install the APK, determine the application
  package when needed, launch it, and connect UI/CDP helpers in dependency
  order. `Setup` does not download system images or create/provision AVDs.
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

### M9 — Host Android smoke and lightweight emulator profile

Files: root/emulator configuration and tests, examples, and status documents.

Host tooling was discovered outside the initial workspace sandbox. One
snapshot-free `Medium_Phone_API_36.0` run passed the root-owned lifecycle, UI,
CDP, restart/reconnect, and teardown smoke, but caused unacceptable host lag.
Two `Pixel_7` attempts cold-booted with the known headless profile; one reached
a bounded UI-dump timeout and the other returned a soft `am start -W` status
timeout. Both emulators were cleaned up. The DocumentsUI multi-file roundtrip
was not completed, and the user stopped further live execution because even
the smaller attempt made the host lag.

Acceptance criteria:

- Offer a convenient headless AVD configuration that disables the window,
  audio, boot animation, and snapshots, caps the emulator at two virtual CPUs,
  selects GPU mode automatically, requires VM acceleration, and does not
  override image-managed memory.
- Keep raw configuration fields available when a consumer needs a current host
  override such as `swiftshader`.
- Treat `am start -W`'s `Status: timeout` as a soft launch result only while a
  later bounded CDP connection remains responsible for proving readiness.
- State clearly that the library starts and owns an existing AVD but does not
  provision or download one.
- Cover the behavior through deterministic unit and static validation. Do not
  run another existing host AVD during this session.
- Recommend a dedicated CI-oriented `small_phone` AVD: 720x1280,
  `google_apis` x86_64, and non-Play. Run Android integration serially.
- Do not claim the native DocumentsUI file-picker roundtrip passed.

M9 passed deterministic validation on 2026-07-31: the full race suite, vet,
lint (including the integration build tag), native and Windows builds, module
tidiness/verification, tagged example tests, YAML parsing, formatting/diff
checks, and govulncheck all succeeded. No emulator was started for this final
pass.

## External Follow-up

The host does have an Android SDK, emulator, and debuggable WebView fixture, but
its existing phone AVDs are too resource-heavy for continued validation. At the
time of the stopped runs only about 4.2 GiB was available, all 2 GiB of swap was
in use, and the `Pixel_7` API 34 image forced 2560 MiB of guest RAM.

Before retrying the copyable consumer/file-picker example, provision the
dedicated 720x1280 non-Play `small_phone` AVD with a `google_apis` x86_64 image.
Then run one emulator and the integration suite serially. Publishing the local
commits remains intentionally deferred until the user requests it.
