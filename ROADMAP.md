# go-adbtest Roadmap

## Phase 1: Foundation — `adb/` package
> ADB command wrapper. Everything else depends on this.

- [ ] `adb.Client` struct with serial and path auto-detection
- [ ] `Shell`, `Install`, `Push`, `Pull`, `Forward` methods
- [ ] `Devices()`, `WaitForDevice()`, `Screencap()`
- [ ] `testing.TB` helper variants (`ShellOrFail`, etc.)
- [ ] Unit tests

## Phase 2: Emulator Lifecycle — `emulator/` package
> Boot, monitor, and kill emulators.

- [ ] `Config` struct with AVD, GPU, headless, wipe options
- [ ] `Start` — launch emulator process, detect serial
- [ ] `WaitForBoot` — poll `sys.boot_completed`
- [ ] `Kill` — graceful shutdown with SIGKILL fallback
- [ ] Unit tests

## Phase 3: Native UI — `ui/` package
> Interact with Android's native UI via uiautomator.

- [ ] `parse.go` — XML parsing, element search by text/ID/regex
- [ ] `ui.go` — `TapOnText`, `WaitForText`, `AssertVisible`, `AssertGone`
- [ ] Fallback: `/dev/tty` → `/sdcard/ui.xml` dump strategy
- [ ] Unit tests (XML parsing with fixtures)

## Phase 4: Chrome DevTools Protocol — `cdp/` package
> WebView interaction via CDP over WebSocket.

- [ ] `ws.go` — WebSocket connection, message routing by ID
- [ ] `cdp.go` — `Eval`, `Click` (dispatchMouseEvent), `WaitForSelector`
- [ ] Target discovery via `/json/list`
- [ ] Reconnection after app restart
- [ ] Unit tests

## Phase 5: Permissions — `permissions/` package
> Handle Android runtime permission dialogs.

- [ ] `Grant` / `Deny` with API-level detection (23–29 vs 30+)
- [ ] `GrantAll` for multi-permission requests
- [ ] Unit tests

## Phase 6: Top-level API — `testkit.go`
> Compose all packages into the `Device` handle.

- [ ] `Setup` — boot emulator → install APK → launch app → connect CDP
- [ ] `Teardown`, `RestartApp`, `ForceStop`
- [ ] `Config` with sensible defaults
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
