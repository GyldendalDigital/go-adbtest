# go-adbtest Roadmap

Checked items below are implemented in local commits; the complete local build
still requires the final council audit. Detailed status and recovery notes live
in [docs/work-breakdown.md](docs/work-breakdown.md) and
[docs/session.md](docs/session.md).

## Phase 1: Foundation — `adb/` package
> ADB command wrapper. Everything else depends on this.

- [x] `adb.Client` struct with serial and path auto-detection
- [x] `Shell`, `Install`, `Push`, `Pull`, `Forward` methods
- [x] `Devices()`, `WaitForDevice()`, `Screencap()`
- [x] `testing.TB` helper variants (`ShellOrFail`, etc.)
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
- [x] Fallback: `/dev/tty` → `/sdcard/ui.xml` dump strategy
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

- [x] `Grant` / `Deny` with API-level detection (23–29 vs 30+)
- [x] `GrantAll` for multi-permission requests
- [x] Unit tests

## Phase 6: Top-level API — `testkit.go`
> Compose all packages into the `Device` handle.

- [x] `Setup` — boot emulator → install APK → launch app → connect CDP
- [x] `Teardown`, `RestartApp`, `ForceStop`
- [x] `Config` with sensible defaults
- [ ] Integration test example

## Phase 7: Examples & Documentation
> Real-world usage examples and polished docs.

- [ ] Example test suite
- [ ] CI workflow template
- [ ] godoc comments on all public API

---

## Non-Goals (v1)

- iOS/Simulator support
- Visual regression testing
- Performance benchmarking
- Network mocking/interception
- Multiple simultaneous emulators
- Flaky-test retry logic
