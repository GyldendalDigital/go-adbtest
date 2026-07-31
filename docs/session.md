# Development Session

This is the durable recovery log for the initial `go-adbtest` implementation.
Update it before changing tasks and after every material decision or validation
result.

## Final Local Checkpoint — 2026-07-31

- M0 remains the pushed remote baseline; M1 through M8 are complete in focused
  local commits on `master`.
- The complete race, vet, lint, build, Windows cross-build, module, formatting,
  tagged-example, and vulnerability checks pass.
- No commit from the recovered implementation session has been pushed.
- Real Android execution was initially recorded as external because the
  workspace sandbox exposed no SDK, emulator, `adb`, `aapt`, or fixture. The
  host discovery below corrects that boundary.

## Host Android Follow-up — 2026-07-31

The user clarified that Android tooling is installed on the host outside the
workspace sandbox. Read-only host discovery found:

- SDK: `/home/mortenolsrud/Android/Sdk` (`adb` 37.0.0 and build-tools 34–37);
- AVDs: `Medium_35`, `Medium_Phone_API_36.0`, and `Pixel_7`;
- existing runbook: `ordnett_pluss_v4/scripts/android-run.sh`, defaulting to
  `Medium_Phone_API_36.0`;
- debuggable x86_64 APK: `ordnett_pluss_v4/bin/ordnett_pluss_v4.apk` (also
  present in Gradle debug outputs), package `no.gyldendal.ordnett`, launch
  activity `com.wails.app.MainActivity`.

No emulator is currently running. The next action is a real owned-AVD smoke of
root setup/teardown, UI hierarchy access, CDP evaluation, and restart/reconnect.
The application source has no identified deterministic DOM control that
requests a runtime permission, so the generic permission-flow example remains
separate unless live inspection reveals such a control.

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
5. There is no Android SDK, `adb`, emulator, or `aapt` available in this local
   environment. Unit behavior must be covered with fakes; real Android
   integration remains an explicitly recorded external validation step.
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
| 2026-07-31 | Checked Android tooling | No local SDK tools or Android environment variables found |
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
| 2026-07-31 | Recorded external validation boundary | A real APK/emulator smoke test could not run because Android tooling and a fixture are absent; no local unit or static check remains blocked |

## Next Action

The initial local build is complete. Review the local commits, then run the
consumer example against a real debuggable WebView APK on an Android emulator or
device. Push, PR creation, or other GitHub changes require a new user request.
