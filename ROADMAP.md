# go-adbtest Roadmap

Checked items below are implemented in local commits. The feature build,
examples, documentation, council review, and local release validation are
complete through Phase 9. Host Android tooling and a debuggable fixture were
later found outside the initial workspace sandbox; Phase 9 bounded live work
when the existing AVDs caused unacceptable host pressure. Detailed status
lives in
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

- [x] `Config` struct with AVD, GPU, headless, audio, snapshot, wipe, and
  resource options
- [x] `Start` — launch emulator process, detect serial
- [x] `WaitForBoot` — poll `sys.boot_completed`
- [x] `Kill` — graceful shutdown with SIGKILL fallback
- [x] Lightweight headless profile with two virtual CPUs, required
  acceleration, and image-managed memory
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

- [x] `Setup` — start an existing configured AVD or attach by serial, install,
  launch, connect CDP
- [x] `Teardown`, `RestartApp`, `ForceStop`
- [x] `Config` with sensible defaults
- [x] Explicit secondary-process WebView selection
- [x] Explicit boundary: AVD/system-image provisioning remains consumer-owned
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
- [x] Initial Android sandbox boundary recorded and later corrected by host discovery

## Phase 9: Host Smoke & Constrained Emulator Profile
> Validate the root lifecycle without making a phone-sized emulator the
> library's CI default.

- [x] Discover host SDK, AVDs, and debuggable x86_64 WebView APK
- [x] Expose snapshot-free root setup after quickboot revealed the missing option
- [x] Pass one snapshot-free Medium API 36 root lifecycle/UI/CDP/reconnect smoke
- [x] Stop live validation after Medium and Pixel_7 AVDs caused unacceptable host lag
- [x] Treat `am start -W` soft status timeout as pending bounded CDP readiness
- [x] Keep the public headless profile lightweight: no window/audio/boot
  animation/snapshots, two CPUs, required VM acceleration, automatic GPU, and
  image-managed memory
- [x] Record that the native DocumentsUI multi-file roundtrip did not complete
- [x] Recommend a 720x1280 non-Play `small_phone` `google_apis` x86_64 AVD for
  later serial CI validation
- [x] Re-run deterministic unit/static checks and create focused local commits

## Phase 10: Explicit Provisioning & Environment Doctor
> Make the safe local/self-hosted setup path repeatable without hiding large
> downloads or emulator resource use.

- [ ] `EnsureAVD` validates, reuses, or creates a matching lightweight AVD
- [ ] System-image installation is explicit and licences remain developer-owned
- [ ] Existing AVDs are verified and never overwritten
- [ ] Provisioned AVDs compose directly with the safe `HeadlessAVD` profile
- [ ] `adbtest doctor` checks tools, host ABI, acceleration, profiles, and AVDs
- [ ] Doctor remains read-only, sequential, bounded, and dependency-free
- [ ] Unit, race, vet, lint, build, tagged-example, and module checks pass

---

## Non-Goals (v1)

- iOS/Simulator support
- Visual regression testing
- Performance benchmarking
- Network mocking/interception
- Multiple simultaneous test devices per suite
- Flaky-test retry logic
