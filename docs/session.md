# Development Session

This is the durable recovery log for the initial `go-adbtest` implementation.
Update it before changing tasks and after every material decision or validation
result.

## Completed M10 Checkpoint — 2026-08-03

The user requested an explicit AVD provisioning path plus a lightweight doctor
command. Work continues locally on `master`; nothing will be pushed and no host
emulator will be started during this milestone. Provisioning, doctor, tests,
public documentation, full static validation, and council closure are complete
locally. No M10 commit has been pushed.

Council decisions:

- `EnsureAVD` remains an explicit operation separate from `Setup`. It validates
  and reuses an exact profile or creates it, but never launches the emulator.
- System-image installation is opt-in because it can download several
  gigabytes. Android SDK licence acceptance remains an explicit developer step.
- The default AVD is `small_phone` + non-Play `google_apis` + the host-native
  ABI. Existing same-name AVDs must match; the library never overwrites them.
- Provisioning requires one stable SDK selected by `ANDROID_HOME` or the legacy
  `ANDROID_SDK_ROOT`; when both are set they must resolve to the same SDK.
  If `ANDROID_USER_HOME`, `ANDROID_EMULATOR_HOME`, or legacy
  `ANDROID_SDK_HOME` relocates Android state, `ANDROID_AVD_HOME` must explicitly
  select the directory used for AVDs.
- Provisioning and doctor readiness support Linux x86_64 and native
  macOS amd64/arm64. Windows provisioning is deliberately rejected for now;
  ordinary `Setup` with an existing Windows AVD remains a separate path.
- The returned AVD handle composes with `HeadlessAVD`, which remains the single
  source of the safe no-window/no-audio/no-snapshot/two-core/accelerated launch
  flags.
- `adbtest doctor` is a stdlib-only, read-only command. It runs bounded checks
  sequentially and never downloads packages, accepts licences, starts an
  emulator, contacts a device, or changes AVD state.
- Current Command-line Tools still provide the deterministic `sdkmanager` and
  `avdmanager` contracts needed here. The newer Android CLI is not used because
  its documented emulator creation command does not yet expose a stable AVD
  name, API, target, and architecture across supported hosts.

## Final Local Checkpoint — 2026-07-31

- M0 remains the pushed remote baseline; M1 through M9 are complete in focused
  local commits on `master`.
- The complete race, vet, lint, build, Windows cross-build, module, formatting,
  tagged-example, YAML, and vulnerability checks pass after the host follow-up.
- No commit from the recovered implementation session has been pushed.
- Android tooling was initially recorded as absent because it was outside the
  workspace sandbox. The host discovery and partial live validation below
  correct that boundary.

## Host Android Follow-up — 2026-07-31

The user clarified that Android tooling is installed on the host outside the
workspace sandbox. Read-only host discovery found:

- SDK: `/home/mortenolsrud/Android/Sdk` (`adb` 37.0.0 and build-tools 34–37);
- AVDs: `Medium_35`, `Medium_Phone_API_36.0`, and `Pixel_7`;
- existing runbook: `ordnett_pluss_v4/scripts/android-run.sh`, defaulting to
  `Medium_Phone_API_36.0`;
- debuggable x86_64 APK:
  `ordnett_pluss_v4/build/android/app/build/outputs/apk/debug/app-debug.apk`,
  package `no.gyldendal.ordnett`, launch activity
  `com.wails.app.MainActivity`.

The live validation produced useful but deliberately bounded results:

- A first `Medium_Phone_API_36.0` quickboot exposed the missing root
  `NoSnapshot` option. A subsequent snapshot-free run completed the full root
  smoke once: owned lifecycle, install/launch, UI hierarchy access, CDP
  evaluation, restart/reconnect, and teardown all passed. It nevertheless
  caused unacceptable host lag.
- `Pixel_7` cold-booted with the known headless profile. One attempt reached the
  app but a UI dump exhausted its 10-second bound. A later attempt received
  Android's soft `Status: timeout` from `am start -W`; launch now treats that as
  pending rather than fatal and requires CDP readiness to prove the app usable.
- Both emulators were cleaned up. No emulator was left running.
- The planned DocumentsUI three-file selection roundtrip was not completed.
  Even `Pixel_7` caused enough lag that the user stopped further live runs, so
  this session must not claim an end-to-end file-picker pass.

Host pressure explains why the existing AVDs are unsuitable as a default CI
fixture: only about 4.2 GiB of memory was available, the full 2 GiB swap was in
use, and the `Pixel_7` API 34 image forced 2560 MiB of guest RAM. The lightweight
library profile therefore keeps the emulator headless, disables audio,
animations, and snapshots, caps virtual CPUs at two, requires VM acceleration,
uses emulator-managed GPU selection, and leaves guest memory to the AVD/image
instead of layering another override on the command line. Requiring acceleration
fails fast instead of silently falling back to CPU emulation. `Setup` starts and
owns an already configured AVD; it does not download a system image or
create/provision an AVD.

For a later live validation, create a dedicated non-Play `small_phone` AVD with
a 720x1280 display and a `google_apis` x86_64 image. Run only that AVD, serially,
and do not repeat live execution in this session.

## Recovery Checkpoint — 2026-07-31

The previous agent session ended while implementing `permissions/`. Repository
state was reconstructed before resuming:

- Branch: `master`
- Remote baseline: `origin/master` at `f307813`
- Local HEAD: `41abba5`, four commits ahead of the remote
- Local committed packages: `adb/`, `emulator/`, `ui/`, and `cdp/`
- Uncommitted state: untracked `permissions/permissions.go` and
  `permissions/permissions_test.go`
- No `testkit.go`, root tests, or `examples/` existed
- No earlier session or work-breakdown document existed

At recovery, the permissions production file built, but its test package did
not compile because `TestResolveTimeout_Custom` passed `[]int64` where
`[]time.Duration` was required. Consequently both `go test ./...` and
`go vet ./...` failed at that compile error. Tests for the four previously
committed packages passed without the permissions package.

## Working Agreement

- Continue directly on `master` for this initial build.
- Commit each package/task separately, but keep every new commit local.
- Do not push, create PRs, or modify GitHub state without a new user request.
- Follow the council protocol, tests-first/alongside implementation, and the
  validation requirements in the repository instructions.
- Preserve unrelated/user changes. At the recovery checkpoint the only
  uncommitted files were the permissions files and recovery documents.

This local-only workflow intentionally overrides the normal branch-and-push
steps in `.github/instructions/*.instructions.md` for the initial build.

## Recovered Council Findings (resolved)

These findings determined the task ordering below. Later M8 review added and
resolved bounded helper calls, Android 14 permission choices, safe WebView
socket/forward ownership, current emulator GPU defaults, and all lint findings.

1. The root package is `adbtest`, matching the module, README, examples, and
   `doc.go`; the stale `package testkit` specification was corrected.
2. Boolean options preserve ordinary Go zero-value semantics. Non-zero defaults
   are documented instead of silently overriding explicit `false` values.
3. App launch uses APK metadata or generic package-manager activity resolution,
   with no Wails-specific assumption; callers can set `AppActivity` explicitly.
4. Root setup supports both an owned `AVD` and an explicit attached `Serial`.
5. Android SDK tools were not visible inside the initial workspace sandbox but
   do exist on the host. Deterministic unit behavior remains covered with fakes;
   host execution is a separate, resource-sensitive validation step.
6. Recovery review found three blockers in already committed code: normal
   `/dev/tty` UI dumps included trailing status text that broke XML parsing;
   CDP protocol/disconnect failures were reported as success; and CDP HTTP
   discovery had no request timeout.
7. Emulator shutdown ran `emu kill` through `adb shell` instead of the adb
   emulator command, and failure paths did not reliably reap the child process.
8. README and `go.mod` disagreed on the minimum Go version (`1.23+` versus
   `go 1.26.5`).

## Execution Log

| Date | Event | Result |
| --- | --- | --- |
| 2026-07-31 | Reconstructed git state, plans, instructions, and public spec | Found four local commits and unfinished untracked permissions files |
| 2026-07-31 | Ran `go test -count=1 ./...` | Failed only because the recovered permissions test does not compile |
| 2026-07-31 | Ran `go vet ./...` | Failed at the same permissions test compile error |
| 2026-07-31 | Checked sandbox-visible Android tooling | No SDK tools or Android environment variables were exposed inside the initial sandbox; later host discovery corrected this boundary |
| 2026-07-31 | Committed recovery plan | Local commit `2037f27`; nothing pushed |
| 2026-07-31 | Ran five-specialist recovery audit | Added focused UI, CDP, and adb/emulator stabilization tasks before root composition |
| 2026-07-31 | Completed M5 permission handling | Race tests pass; controller filtering, API variants, errors, dialog races, sequential grants, and safety cap are covered |
| 2026-07-31 | Completed M5A UI stabilization | Fake-adb tests cover normal trailing dump status, pull fallback/cleanup, invalid bounds, and shell-safe text entry |
| 2026-07-31 | Completed M5C adb/emulator stabilization | Context-aware adb APIs, one boot deadline, early-exit reporting, correct host-side shutdown, idempotence, and process reaping are race-tested |
| 2026-07-31 | Completed M5B CDP stabilization | Protocol/disconnect errors, concurrent routing/close, bounded discovery/readiness, forward cleanup, and visible-selector behavior are race-tested |
| 2026-07-31 | Completed M6 root device API | AVD/attached modes, preflight discovery, activity resolution, bounded orchestration, cleanup ordering, restart, and idempotent teardown pass full race tests |
| 2026-07-31 | Completed M7 examples and public documentation | Added a tagged no-SDK compile check, consumer emulator workflow, reconciled README/spec, Go 1.23 CI baseline, and complete exported godoc |
| 2026-07-31 | Began M8 council audit | Found unbounded UI/permission/CDP helper calls, incomplete modern permission choices, fragile WebView socket selection, a deprecated emulator GPU default, and a locally reproducible golangci-lint failure; focused fixes are required before final validation |
| 2026-07-31 | Hardened Android lifecycle and native interaction | Commit `31b3564` adds process-group cleanup, finite UI/permission deadlines, unique UI dump paths, current permission-controller choices, and the `auto` GPU default |
| 2026-07-31 | Hardened CDP process/socket selection and ownership | Commit `820400d` adds explicit `AppProcess`, all-PID socket discovery, exclusive owned forwards, bounded evaluation/waits, and the maintained WebSocket dependency |
| 2026-07-31 | Closed static/CI findings | Commit `9019bb5` leaves golangci-lint at zero findings and compile-checks the tagged example in the required CI job |
| 2026-07-31 | Ran final local validation | Race tests, vet, lint, native and Windows builds, module tidy/verify, formatting/diff checks, tagged example, and govulncheck all pass |
| 2026-07-31 | Recorded initial external validation boundary | Android tooling was unavailable inside the sandbox; later host discovery superseded the claim that it was absent from the machine |
| 2026-07-31 | Discovered host Android fixture | Found SDK 37.0.0, three existing AVDs, and a debuggable x86_64 WebView APK outside the workspace sandbox |
| 2026-07-31 | Ran snapshot-free Medium API 36 root smoke | Owned boot/install/launch, UI, CDP, restart/reconnect, and teardown passed once, but the AVD caused unacceptable host lag |
| 2026-07-31 | Bounded Pixel_7 validation | Cold boot succeeded; one run timed out during UI dump and another returned a soft launch timeout. Both emulators were cleaned up and further live runs were stopped |
| 2026-07-31 | Exposed snapshot-free root setup | Commit `b418420` passes `NoSnapshot` through the root API so owned runs can avoid unstable quickboot state |
| 2026-07-31 | Hardened delayed app launch | Commit `a2522ef` accepts Activity Manager's advisory `Status: timeout` while CDP remains the bounded readiness proof |
| 2026-07-31 | Added constrained owned-AVD profile | Commit `e503bb0` adds `HeadlessAVD`, a two-core cap, GPU auto-selection, optional RAM override, and root/emulator pass-through tests |
| 2026-07-31 | Prevented unaccelerated safe-profile boots | Commit `6efa95b` exposes validated acceleration modes and makes `HeadlessAVD` use `-accel on`, failing fast without a usable hypervisor |
| 2026-07-31 | Added lightweight cross-boundary example | Commit `3999a1c` keeps lifecycle/CDP always on, makes DocumentsUI and permissions opt-in, uses unique fixtures, and supplies a small-phone serial CI template |
| 2026-07-31 | Completed final M9 validation | Full race, vet, lint (normal and tagged), builds, module checks, tagged example, YAML parsing, diff checks, and govulncheck pass; no emulator was started |
| 2026-07-31 | Expanded public AVD and API guidance | README now distinguishes AVD names, hardware profiles, and ADB serials; documents Android Studio, CLI, and deterministic CI selection; and explains CDP, native UI, permissions, lifecycle, and ADB usage. No emulator was started |
| 2026-08-03 | Began M10 provisioning and doctor design | Council selected explicit safe-profile provisioning, opt-in image installation, strict non-overwriting reuse, and a read-only stdlib doctor; no emulator was started |
| 2026-08-03 | Implemented explicit AVD provisioning | Added shared SDK resolution/command helpers plus strict profile validation, opt-in image installation, non-overwriting creation, post-create verification, and direct safe-launch composition; targeted race, vet, lint, format, and diff checks pass without starting an emulator |
| 2026-08-03 | Implemented the environment doctor | Added sequential bounded checks, actionable remediation, stable report labels/exit codes, and deterministic command tests; no emulator was started |
| 2026-08-03 | Documented the M10 consumer path | README and implementation spec now distinguish provisioning from Setup, show safe headless composition and doctor usage, and retain deterministic Android Studio/CLI/CI selection guidance; no live Android validation was performed |
| 2026-08-03 | Closed independent provisioning reviews | Commits `c8362d1` and `9e5d936` enforce one coherent SDK/AVD home, host-native metadata, root-serialized creation, collision recovery, current Android home variables, non-destructive profile preflight, and a post-install retry for image-contributed profiles |
| 2026-08-03 | Committed the read-only doctor | Commit `9f85bbf` adds the CLI and fake-driven checks; its Android command allowlist is version, list, and acceleration diagnostics only |
| 2026-08-03 | Completed final M10 validation | Full race tests with coverage, vet, lint with and without the integration tag, native and Windows builds, tagged example, module tidy/verify, formatting, YAML parsing, diff checks, and govulncheck pass; final council and API audits have no unresolved blockers |

## Next Action

M10 is complete locally. Keep every commit local until the user explicitly
requests a push. A later, separately authorized live integration run should
provision and use one 720x1280 `small_phone`, non-Play `google_apis`
host-native AVD, run serially, and stop if host pressure returns. Push, PR
creation, or other GitHub changes require a new user request.
