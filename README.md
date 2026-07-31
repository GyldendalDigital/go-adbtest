# go-adbtest

A Go library for integration-testing Android WebView apps on real emulators.

Write `go test` functions that drive a real Android emulator — interact with both native UI (permission dialogs, file pickers, system chrome) and WebView content (DOM elements, JS evaluation) — using semantic text/selector matching instead of fragile coordinates.

Follows the Go stdlib naming pattern (`httptest`, `fstest`, `iotest`).

## Quick Start

```go
import adbtest "github.com/GyldendalDigital/go-adbtest"

var device *adbtest.Device

func TestMain(m *testing.M) {
    device = adbtest.Setup(adbtest.Config{
        AVD:      "Pixel_7",
        APK:      "bin/myapp.apk",
        Headless: true,
        GPU:      "swiftshader_indirect",
    })
    code := m.Run()
    device.Teardown()
    os.Exit(code)
}

func TestLoginFlow(t *testing.T) {
    device.RestartApp(t)
    device.CDP.Click(t, "#login-btn")
    device.UI.TapOnText(t, "While using the app", 10*time.Second)
    result := device.CDP.Eval(t, `document.getElementById("status").textContent`)
    if !strings.Contains(result, "logged in") {
        t.Fatalf("expected 'logged in', got %q", result)
    }
}
```

## Features

- **Native UI interaction** via `uiautomator` — tap buttons by text, wait for elements, assert visibility
- **WebView interaction** via Chrome DevTools Protocol — evaluate JS, click elements with real user gestures, wait for selectors
- **Permission handling** — automatically grant/deny runtime permission dialogs across API levels 23–35
- **Emulator lifecycle** — boot, configure, and tear down emulators from your tests
- **ADB wrapper** — shell commands, APK install, file push/pull, port forwarding, screenshots
- **CI-ready** — works with [`reactivecircus/android-emulator-runner`](https://github.com/ReactiveCircus/android-emulator-runner) GitHub Action

## Architecture

```
go-adbtest/
├── testkit.go          # Setup/Teardown, Config, Device (top-level API)
├── adb/                # ADB command wrapper
├── emulator/           # Emulator lifecycle management
├── ui/                 # Native UI interaction (uiautomator)
├── cdp/                # Chrome DevTools Protocol client (WebView)
└── permissions/        # Android permission dialog helper
```

Each package is independently usable, but the root `testkit` package composes them into a batteries-included `Device` handle.

## Installation

```sh
go get github.com/GyldendalDigital/go-adbtest@latest
```

## Prerequisites

- Go 1.23+
- Android SDK with `platform-tools` (for `adb`)
- An Android emulator AVD, or a running emulator/device

## Build Tags

Tests that require a running emulator use the `android_integration` build tag:

```go
//go:build android_integration
```

Run them with:

```sh
go test -tags android_integration -timeout 5m ./...
```

## CI Integration

```yaml
# .github/workflows/android-test.yml
jobs:
  android-integration:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.23' }
      - uses: reactivecircus/android-emulator-runner@v2
        with:
          api-level: 35
          arch: x86_64
          emulator-options: -no-window -gpu swiftshader_indirect -no-audio -no-boot-anim
          script: go test -tags android_integration -timeout 5m ./...
```

## Dependencies

Minimal — only one runtime dependency:

- [`nhooyr.io/websocket`](https://github.com/nhooyr/websocket) — WebSocket client for CDP communication

## Documentation

- [Implementation Spec](docs/implementation-spec.md) — full API specification and design decisions
- [Roadmap](ROADMAP.md) — development phases and status

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development guidelines.

## License

[MIT](LICENSE) © Gyldendal Digital
