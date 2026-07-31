# go-adbtest Roadmap

Checked items below are implemented in local commits. The feature build,
examples, documentation, council review, and local release validation are
complete. Real Android execution remains an external follow-up because this
host has no SDK, emulator, or application fixture. Detailed status lives in
[docs/work-breakdown.md](docs/work-breakdown.md) and
[docs/session.md](docs/session.md).

## Phase 1: Foundation — `adb/` package
> ADB command wrapper. Everything else depends on this.

- [x] `adb.Client` struct with serial and path auto-detection
- [x] `Shell`, `Install`, `Push`, `Pull`, `Forward` methods
- [x] `Devices()`, `WaitForDevice()`, `Screencap()`
- [x] `testing.TB` helper (`ShellOrFail`)
- [x] Unit tests

## Phase 2: Emulator Lifecycle — `emulator/` package
> Boot, monitor, and kill emulators.

- [x] `Config` struct with AVD, GPU, headless, wipe options
- [x] `Start` — launch emulator process, detect serial
- [x] `WaitForBoot` — poll `sys.boot_completed`
- [x] `Kill` — graceful shutdown with SIGKILL fallback
- [x] Unit tests

## Phase 3: Native UI — `ui/` package
> Interact with Android's native UI via uiautomator.

- [x] `parse.go` — XML parsing, element search by text/ID/regex
- [x] `ui.go` — `TapOnText`, `WaitForText`, `AssertVisible`, `AssertGone`
- [x] Fallback: `/dev/tty` → unique device-file/pull dump strategy
- [x] Unit tests (XML parsing with fixtures)

## Phase 4: Chrome DevTools Protocol — `cdp/` package
> WebView interaction via CDP over WebSocket.

- [x] `ws.go` — WebSocket connection, message routing by ID
- [x] `cdp.go` — `Eval`, `Click` (dispatchMouseEvent), `WaitForSelector`
- [x] Target discovery via `/json/list`
- [x] Reconnection after app restart
- [x] Unit tests

## Phase 5: Permissions — `permissions/` package
> Handle Android runtime permission dialogs.

- [x] `Grant` / `Deny` across permission-controller button variants
- [x] Explicit `GrantSelected` for Android's limited-media flow
- [x] `GrantAll` for multi-permission requests
- [x] Unit tests

## Phase 6: Top-level API — `testkit.go`
> Compose all packages into the `Device` handle.

- [x] `Setup` — start an AVD or attach by serial, install, launch, connect CDP
- [x] `Teardown`, `RestartApp`, `ForceStop`
- [x] `Config` with sensible defaults
- [x] Explicit secondary-process WebView selection
- [x] Integration test example

## Phase 7: Examples & Documentation
> Real-world usage examples and polished docs.

- [x] Example test suite
- [x] CI workflow template
- [x] godoc comments on all public API

## Phase 8: Council Review & Local Validation
> Cross-package correctness and release-quality checks.

- [x] Go, Android, CDP, test-infrastructure, and code-quality review
- [x] Full race, vet, lint, build, module, formatting, and vulnerability checks
- [x] SDK-free tagged example check in required CI
- [x] External Android validation boundary recorded

---

## Non-Goals (v1)

- iOS/Simulator support
- Visual regression testing
- Performance benchmarking
- Network mocking/interception
- Multiple simultaneous test devices per suite
- Flaky-test retry logic
