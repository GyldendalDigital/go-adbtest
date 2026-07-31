# go-adbtest

`go-adbtest` is a Go library for integration-testing Android WebView apps on a
real emulator or an explicitly selected attached device. Tests can cross the
native/WebView boundary: use `uiautomator` for Android UI and the Chrome
DevTools Protocol (CDP) for DOM interaction and JavaScript evaluation.

The module follows the Go standard-library naming style of `httptest`,
`fstest`, and `iotest`.

## Installation

```sh
go get github.com/GyldendalDigital/go-adbtest@latest
```

## Quick start

The root package is `adbtest`. `Setup` is intended for `TestMain`; it validates
the configuration, starts or attaches to one device, installs and launches the
APK, and waits for the WebView CDP endpoint.

```go
package androidtest

import (
    "fmt"
    "os"
    "strings"
    "testing"
    "time"

    adbtest "github.com/GyldendalDigital/go-adbtest"
)

var device *adbtest.Device

func TestMain(m *testing.M) {
    device = adbtest.Setup(adbtest.Config{
        AVD:        "Pixel_7",
        APK:        "bin/myapp.apk",
        AppPackage: "com.example.myapp",
        Headless:   true,
        GPU:        "software",
        NoAudio:    true,
        NoSnapshot: true,
    })

    code := m.Run()
    if err := device.Teardown(); err != nil {
        fmt.Fprintln(os.Stderr, "tear down Android test device:", err)
        if code == 0 {
            code = 1
        }
    }
    os.Exit(code)
}

func TestLoginFlow(t *testing.T) {
    device.RestartApp(t)
    device.CDP.Click(t, "#login-btn")
    device.Permissions.Grant(t, 10*time.Second)

    result := device.CDP.Eval(t,
        `document.getElementById("status").textContent`)
    if !strings.Contains(result, "logged in") {
        t.Fatalf("expected login status, got %q", result)
    }
}
```

`RestartApp` force-stops and relaunches the process, then reconnects CDP. It
does not clear application data; tests that require cleared data must do so
explicitly.

## Setup modes and defaults

Exactly one device mode is required:

- `AVD` starts an emulator owned by the `Device`. `Teardown` stops it.
- `Serial` attaches to an already-running emulator or device. `Teardown`
  closes CDP and force-stops the launched app, but does not stop that device.

Selection is always explicit; `Setup` never guesses among connected devices.
AVD-only fields (`Headless`, `GPU`, `NoAudio`, `WipeData`, and `NoSnapshot`)
must not be set with `Serial`.

The zero value of every boolean remains `false`. Set `Headless: true` and
`NoAudio: true` explicitly when desired. Other zero values receive these
defaults:

| Field | Default |
| --- | --- |
| `GPU` | `auto` in AVD mode |
| `BootTimeout` | 120 seconds |
| `AppTimeout` | 30 seconds |
| `CDPPort` | 9222 |

For a conservative headless CI profile, set `Headless: true`,
`GPU: "software"`, `NoAudio: true`, and `NoSnapshot: true`. `NoSnapshot`
forces a cold boot and prevents a failed run from saving unstable quick-boot
state.

`APK` is always required. `AppPackage` may be omitted when Android `aapt` is
available; package inspection happens before an emulator is started. `aapt`
is found through `AAPT`, `PATH`, or the newest build-tools installation under
`ANDROID_HOME`/`ANDROID_SDK_ROOT`. If `AppPackage` is supplied, `aapt` is not
required.

`AppActivity` is optional. When Setup inspects the APK it first uses the
discovered launchable activity; otherwise it falls back to Android's
`cmd package resolve-activity` and `pm resolve-activity`. An explicit activity
may be a short class, a dot-prefixed class, a fully qualified class, or a
matching package/component. Set it explicitly for the most predictable behavior
on older Android releases.

`AppProcess` selects the process that hosts the debuggable WebView. It defaults
to `AppPackage`; a relative secondary process such as `:webview` is expanded to
`com.example.myapp:webview`. A fully qualified process name is also accepted.
Setup matches every PID for that process against the device's actual WebView
DevTools sockets and reports ambiguous matches instead of guessing.

Setup cleans up resources acquired before any later failure. `Teardown` is
idempotent and continues cleanup after individual errors. App launch, PID
discovery, port forwarding, target discovery, and CDP connection use bounded
contexts rather than fixed sleeps. CDP forwarding is exclusive: `Setup` fails
instead of replacing an existing mapping for `CDPPort`, and teardown removes
only a mapping created by this library.

## Attached-device setup

Use `Serial` when another tool owns the emulator:

```go
device = adbtest.Setup(adbtest.Config{
    Serial:     os.Getenv("ADBTEST_SERIAL"),
    APK:        os.Getenv("ADBTEST_APK"),
    AppPackage: "com.example.myapp",
})
```

This is the appropriate mode for `reactivecircus/android-emulator-runner`,
which has already started the emulator before its script runs.

## Features

- Native UI interaction by text or resource ID through `uiautomator`
- WebView evaluation, visibility waits, and real-gesture clicks through CDP
- Runtime permission grant/deny handling across Android permission-controller
  variants
- Owned AVD lifecycle or explicit attachment to an existing device
- Context-aware ADB and CDP primitives for lower-level composition
- Cleanup of WebSocket connections, ADB forwards, app processes, and owned
  emulator processes

Each subpackage is independently usable:

```text
go-adbtest/
├── testkit.go          # root package adbtest: Config, Device, Setup
├── adb/                # context-aware ADB commands and device discovery
├── emulator/           # emulator start, boot monitoring, and shutdown
├── ui/                 # native UI parsing and interaction
├── cdp/                # WebView CDP transport and high-level actions
└── permissions/        # Android runtime-permission dialogs
```

## Prerequisites

- Go 1.23 or later
- Android SDK platform-tools (`adb`)
- For AVD mode: the Android emulator binary and a configured AVD
- For package auto-detection: Android build-tools (`aapt`)
- A debuggable WebView whose app enables WebView debugging

## Testing

Unit tests use deterministic fakes and require no Android SDK:

```sh
go test ./...
```

Consumer tests that require Android should use the `android_integration` build
tag:

```go
//go:build android_integration
```

```sh
go test -tags android_integration -timeout 5m ./...
```

## CI with an attached emulator

The emulator runner owns the emulator, so the test configuration must use the
serial exported below rather than `AVD`:

```yaml
jobs:
  android-integration:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version: '1.23.x'
      - uses: reactivecircus/android-emulator-runner@v2
        with:
          api-level: 35
          arch: x86_64
          emulator-options: >-
            -no-window -gpu software -no-audio -no-boot-anim
          script: |
            ./your-android-build-command
            export ADBTEST_APK="$PWD/path/to/app.apk"
            export ADBTEST_SERIAL="$(adb devices | awk 'NR > 1 && $2 == "device" { print $1; exit }')"
            test -n "$ADBTEST_SERIAL"
            go test -tags android_integration -timeout 5m ./...
```

`Setup` installs `ADBTEST_APK`; the CI build step should build it but need not
run a separate `adb install`. Installation uses `adb install -r`, so an existing
installation's application data and granted permissions are retained. A
complete, copyable consumer test and workflow is available in
[examples](examples/README.md).

## Dependencies

The only runtime dependency is
[`github.com/coder/websocket`](https://github.com/coder/websocket).

## Documentation

- [Implementation specification](docs/implementation-spec.md)
- [Android integration example](examples/README.md)
- [Roadmap](ROADMAP.md)
- [Contributing](CONTRIBUTING.md)

## License

[MIT](LICENSE) © Gyldendal Digital
