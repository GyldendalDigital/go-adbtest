# go-adbtest: Implementation Spec for a Go Android WebView Integration Test Library

## Purpose

A standalone Go library that lets you write `go test` functions which drive a real Android emulator — interacting with both native UI (permission dialogs, file pickers, system chrome) and WebView content (DOM elements, JS evaluation) — using semantic text/selector matching instead of fragile coordinates.

## Target Consumer

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
    device.UI.TapOnText(t, "While using the app", 10*time.Second) // permission
    result := device.CDP.Eval(t, `document.getElementById("status").textContent`)
    assert.Contains(t, result, "logged in")
}
```

## Architecture

```
go-adbtest/
├── go.mod                    # module github.com/GyldendalDigital/go-adbtest
├── go.sum
├── testkit.go                # Setup/Teardown, Config, Device struct (top-level API)
├── adb/
│   ├── adb.go               # Core: shell, push, pull, install, forward, devices
│   └── adb_test.go
├── emulator/
│   ├── emulator.go          # Start, WaitForBoot, Kill, WipeData
│   └── emulator_test.go
├── ui/
│   ├── ui.go                # TapOnText, LongPressOnText, WaitForText, AssertVisible, AssertGone
│   ├── parse.go             # uiautomator XML parsing → element bounds
│   └── ui_test.go
├── cdp/
│   ├── cdp.go               # Connect, Evaluate, Click (CSS selector), WaitForSelector, DispatchMouseEvent
│   ├── ws.go                # WebSocket lifecycle, reconnection, message routing
│   └── cdp_test.go
├── permissions/
│   ├── permissions.go       # GrantPermission, DenyPermission (composed from ui.TapOnText)
│   └── permissions_test.go
└── examples/
    └── wails_test.go        # Full example showing usage with a Wails app
```

## Package-by-Package Specification

---

### `adb/` — ADB command wrapper

**File: `adb.go`**

```go
package adb

// Client wraps adb commands targeting a specific device/emulator.
type Client struct {
    Serial string // e.g. "emulator-5554"; empty = first device
    ADBPath string // auto-detected from $ANDROID_HOME/platform-tools/adb or PATH
}

// New creates a Client, auto-detecting adb path.
func New(serial string) (*Client, error)

// Shell runs `adb shell <cmd>` and returns combined stdout+stderr.
func (c *Client) Shell(cmd string) (string, error)

// ShellOrFail is Shell but calls t.Fatal on error.
func (c *Client) ShellOrFail(t testing.TB, cmd string) string

// Install installs an APK (-r for reinstall).
func (c *Client) Install(apkPath string) error

// Push pushes a local file to the device.
func (c *Client) Push(local, remote string) error

// Pull pulls a device file to local.
func (c *Client) Pull(remote, local string) error

// Forward sets up a TCP port forward: `adb forward tcp:<local> localabstract:<remote>`.
func (c *Client) Forward(localPort int, abstractSocket string) error

// Screencap captures a PNG screenshot to a local file.
func (c *Client) Screencap(localPath string) error

// Devices returns all connected device serials.
func Devices() ([]string, error)

// WaitForDevice blocks until a device is connected (with timeout).
func (c *Client) WaitForDevice(timeout time.Duration) error
```

**Key implementation notes:**
- Use `exec.Command` with `adb -s <serial>` prefix when serial is set.
- Trim `\r\n` from shell output (Android shells return `\r\n`).
- `ADBPath` resolution order: `$ANDROID_HOME/platform-tools/adb` → `$ANDROID_SDK_ROOT/platform-tools/adb` → `adb` in PATH.

---

### `emulator/` — Emulator lifecycle

**File: `emulator.go`**

```go
package emulator

type Config struct {
    AVD       string        // AVD name (e.g. "Pixel_7")
    Headless  bool          // -no-window
    GPU       string        // "swiftshader_indirect" (safe), "host" (fast but crash-prone)
    NoAudio   bool          // -no-audio
    WipeData  bool          // -wipe-data (clean state)
    NoSnapshot bool         // -no-snapshot
    Timeout   time.Duration // boot timeout (default 120s)
}

type Instance struct {
    PID    int
    Serial string // e.g. "emulator-5554"
    ADB    *adb.Client
}

// Start boots an emulator with the given config. Blocks until boot_completed=1.
func Start(cfg Config) (*Instance, error)

// Kill stops the emulator (adb emu kill).
func (i *Instance) Kill() error

// IsRunning checks if the emulator process is still alive.
func (i *Instance) IsRunning() bool

// WaitForBoot polls sys.boot_completed with a 1s interval until timeout.
func (i *Instance) WaitForBoot(timeout time.Duration) error
```

**Key implementation notes:**
- `Start` runs `emulator -avd <name> [flags] &` via `exec.Command`, captures PID.
- Flags composed from Config: `-no-window`, `-gpu swiftshader_indirect`, `-no-audio`, `-no-boot-anim`, `-no-snapshot`.
- Wait for boot by polling `adb shell getprop sys.boot_completed` every 1s.
- Serial detection: after `adb wait-for-device`, run `adb devices` and pick the one that appeared.
- `Kill` sends `adb emu kill` then waits for process exit (with 10s timeout before SIGKILL).

---

### `ui/` — Native UI interaction via uiautomator

**File: `parse.go`**

```go
package ui

// Element represents a UI element from the hierarchy dump.
type Element struct {
    Text        string
    ResourceID  string
    ContentDesc string
    Class       string
    Package     string
    Clickable   bool
    Bounds      Rect // parsed from bounds="[x1,y1][x2,y2]"
}

type Rect struct {
    X1, Y1, X2, Y2 int
}

func (r Rect) CenterX() int { return (r.X1 + r.X2) / 2 }
func (r Rect) CenterY() int { return (r.Y1 + r.Y2) / 2 }

// ParseDump parses uiautomator XML dump into a flat slice of Elements.
func ParseDump(xmlData []byte) ([]Element, error)

// FindByText returns elements whose Text contains the substring (case-sensitive).
func FindByText(elements []Element, text string) []Element

// FindByResourceID returns elements whose ResourceID matches exactly.
func FindByResourceID(elements []Element, id string) []Element

// FindByTextRegex returns elements whose Text matches the regex.
func FindByTextRegex(elements []Element, pattern string) []Element
```

**File: `ui.go`**

```go
package ui

// Interactor provides native UI interactions via uiautomator + adb input.
type Interactor struct {
    ADB     *adb.Client
    Timeout time.Duration // default wait timeout (10s)
}

// TapOnText finds an element by text and taps its center. Polls with retry.
func (u *Interactor) TapOnText(t testing.TB, text string, timeout ...time.Duration)

// LongPressOnText finds an element by text and long-presses (swipe-in-place 1.5s).
func (u *Interactor) LongPressOnText(t testing.TB, text string, timeout ...time.Duration)

// TapOnID finds an element by resource-id and taps its center.
func (u *Interactor) TapOnID(t testing.TB, resourceID string, timeout ...time.Duration)

// WaitForText polls until an element with the given text appears.
func (u *Interactor) WaitForText(t testing.TB, text string, timeout ...time.Duration)

// AssertVisible asserts that text is currently visible in the UI hierarchy.
func (u *Interactor) AssertVisible(t testing.TB, text string)

// AssertGone asserts that text is NOT visible in the UI hierarchy.
func (u *Interactor) AssertGone(t testing.TB, text string)

// Dump returns the current UI hierarchy as parsed Elements.
func (u *Interactor) Dump() ([]Element, error)

// Tap sends an input tap at absolute coordinates (escape hatch).
func (u *Interactor) Tap(x, y int)

// TypeText types text via `adb shell input text`.
func (u *Interactor) TypeText(text string)
```

**Key implementation notes:**
- `uiautomator dump /dev/tty` outputs XML to stdout (avoids file I/O on device).
- If that fails (some API levels), fall back to `uiautomator dump /sdcard/ui.xml` + `adb pull`.
- Polling interval: 500ms. Default timeout: 10s (overridable per call).
- Parse bounds with regex: `bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"`.
- For `TapOnText`, if multiple elements match, tap the first clickable one; if none clickable, tap the first one's parent (walk up).
- `t.Fatalf` on timeout with a descriptive message including the text sought and what WAS visible.

---

### `cdp/` — Chrome DevTools Protocol client

**File: `ws.go`**

```go
package cdp

// Conn manages a WebSocket connection to a WebView's DevTools endpoint.
type Conn struct {
    url    string
    ws     *websocket.Conn // use nhooyr.io/websocket or gorilla/websocket
    nextID int64
    mu     sync.Mutex
    pending map[int64]chan json.RawMessage
}

// Connect establishes a CDP connection. Discovers the WebSocket URL via
// http://localhost:<port>/json/list (first target).
func Connect(localPort int) (*Conn, error)

// ConnectWithRetry retries connection until success or timeout (handles app startup race).
func ConnectWithRetry(localPort int, timeout time.Duration) (*Conn, error)

// Send sends a CDP command and waits for its response.
func (c *Conn) Send(method string, params map[string]any) (json.RawMessage, error)

// Close closes the WebSocket.
func (c *Conn) Close() error

// Reconnect drops the current connection and re-establishes (after app restart).
func (c *Conn) Reconnect() error
```

**File: `cdp.go`**

```go
package cdp

// Client provides high-level WebView interactions over CDP.
type Client struct {
    Conn      *Conn
    ADB       *adb.Client
    LocalPort int // TCP port forwarded to the WebView debug socket
    PID       int // app PID (for the socket name)
}

// NewClient creates a CDP client, sets up port forwarding, and connects.
func NewClient(adbClient *adb.Client, appPackage string) (*Client, error)

// Eval evaluates a JS expression and returns the string result.
func (c *Client) Eval(t testing.TB, expression string) string

// EvalAsync evaluates a JS expression that returns a Promise, awaiting it.
func (c *Client) EvalAsync(t testing.TB, expression string) string

// Click dispatches a mousePressed+mouseReleased at the center of a CSS-selected element.
// This counts as a user gesture (unlike element.click() which doesn't for file inputs).
func (c *Client) Click(t testing.TB, cssSelector string)

// WaitForSelector polls until a CSS selector matches a visible element.
func (c *Client) WaitForSelector(t testing.TB, cssSelector string, timeout time.Duration)

// WaitForText polls until an element's textContent contains the substring.
func (c *Client) WaitForText(t testing.TB, cssSelector string, text string, timeout time.Duration) string

// Reconnect re-discovers the app PID, re-forwards the port, and reconnects WebSocket.
func (c *Client) Reconnect(t testing.TB)
```

**Key implementation notes:**
- Port forward: `adb forward tcp:<port> localabstract:webview_devtools_remote_<PID>`.
- PID discovery: `adb shell pidof <package>`.
- WebSocket URL discovery: `GET http://localhost:<port>/json/list` → first entry's `webSocketDebuggerUrl`.
- `Click` implementation: get bounding rect via `Runtime.evaluate` + `getBoundingClientRect()`, then `Input.dispatchMouseEvent` (mousePressed + mouseReleased). This is a real user gesture (unlike `element.click()`).
- `Eval` uses `Runtime.evaluate` with `returnByValue: true`. Check for `exceptionDetails` in response → `t.Fatalf`.
- `EvalAsync` uses `Runtime.evaluate` with `awaitPromise: true, returnByValue: true`.
- Message routing: each `Send` gets a unique incrementing ID. A background goroutine reads the WebSocket and dispatches responses to the correct pending channel by ID.
- Use `nhooyr.io/websocket` (pure Go, maintained) — not gorilla (archived).
- Reconnect: close old WS, re-discover PID (may have changed), re-forward, re-connect.

---

### `permissions/` — Android permission dialog helper

**File: `permissions.go`**

```go
package permissions

// Handler knows how to interact with Android runtime permission dialogs.
type Handler struct {
    UI *ui.Interactor
}

// Grant taps the "allow" option on the current permission dialog.
// Handles API-level differences in button wording:
//   API 30+: "While using the app" / "Only this time" / "Don't allow"
//   API 23-29: "Allow" / "Deny"
func (h *Handler) Grant(t testing.TB, timeout ...time.Duration)

// GrantAll repeatedly grants permission dialogs until none appear (for multi-permission requests).
func (h *Handler) GrantAll(t testing.TB, timeout ...time.Duration)

// Deny taps "Don't allow" / "Deny".
func (h *Handler) Deny(t testing.TB, timeout ...time.Duration)
```

**Key implementation notes:**
- `Grant` tries `TapOnText("While using the app")` first (API 30+), falls back to `TapOnText("Allow")` (API 23-29).
- `GrantAll` loops: grant, sleep 1s, check if another dialog appeared, grant again, until no permission dialog text is visible (max 5 iterations to prevent infinite loops).
- Detection of "is a permission dialog showing?" — check for text containing "Allow" AND package `com.google.android.permissioncontroller` or `com.android.permissioncontroller`.

---

### `testkit.go` — Top-level API

**File: `testkit.go`**

```go
package testkit

// Config for setting up a test device.
type Config struct {
    AVD         string        // AVD name
    APK         string        // path to APK to install
    AppPackage  string        // e.g. "com.wails.app" (auto-detected from APK if empty)
    Headless    bool          // no-window (default true)
    GPU         string        // default "swiftshader_indirect"
    NoAudio     bool          // default true
    WipeData    bool          // default false
    BootTimeout time.Duration // default 120s
    AppTimeout  time.Duration // time to wait for app to start after launch (default 30s)
    CDPPort     int           // local port for CDP forwarding (default 9222)
}

// Device is the main handle for interacting with the test device.
type Device struct {
    Emulator    *emulator.Instance
    ADB         *adb.Client
    UI          *ui.Interactor
    CDP         *cdp.Client
    Permissions *permissions.Handler
    Config      Config
}

// Setup boots the emulator, installs the APK, launches the app, and connects CDP.
// Call this from TestMain.
func Setup(cfg Config) *Device

// Teardown kills the emulator and cleans up.
func (d *Device) Teardown()

// RestartApp force-stops and relaunches the app, reconnecting CDP.
// Call this at the start of each test for clean state.
func (d *Device) RestartApp(t testing.TB)

// LaunchApp starts the app without force-stopping first.
func (d *Device) LaunchApp(t testing.TB)

// ForceStop stops the app process.
func (d *Device) ForceStop()
```

**Key implementation notes:**
- `Setup` sequence: `emulator.Start` → `WaitForBoot` → `adb.Install` → launch app → sleep `AppTimeout` → `cdp.NewClient`.
- `RestartApp`: `ForceStop` → sleep 1s → `am start` → sleep `AppTimeout` → `CDP.Reconnect`.
- Package auto-detection from APK: `aapt dump badging <apk> | grep package:`.
- If `aapt` not available, require `AppPackage` in config.
- `Setup` panics on failure (not `t.Fatal`) because it's called from `TestMain` which doesn't have a `*testing.T`.

---

## Dependencies

```
require (
    nhooyr.io/websocket v1.8.x    // WebSocket client for CDP
    github.com/stretchr/testify v1.9.x  // assertions (optional — consumers bring their own)
)
```

Minimal — only the WebSocket library is essential. testify is optional (used in examples/tests).

## Testing the library itself

The library's own tests need a running emulator. Use build tags:

```go
//go:build android_integration

package ui_test
```

Run with: `go test -tags android_integration ./...` (skipped in normal `go test`).

The library tests use a tiny test APK (a single-activity app with known text elements) bundled in `testdata/`.

## CI Integration (for consumers)

```yaml
# .github/workflows/android-test.yml
jobs:
  android-integration:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.25' }
      - uses: reactivecircus/android-emulator-runner@v2
        with:
          api-level: 35
          arch: x86_64
          emulator-options: -no-window -gpu swiftshader_indirect -no-audio -no-boot-anim
          script: |
            # Build and install your app first
            task android:build && task android:assemble:apk
            adb install bin/myapp.apk
            # Run integration tests
            go test -tags android_integration -timeout 5m ./tests/android/
```

## Performance Characteristics

| Operation | Time |
|-----------|------|
| Emulator boot (cold, swiftshader) | ~20s |
| APK install | ~3s |
| First app launch (Go runtime init) | ~10-30s (first time after wipe is slow) |
| App restart (force-stop + relaunch) | ~5s |
| `uiautomator dump` | ~500ms |
| CDP `Evaluate` | ~50ms |
| CDP `DispatchMouseEvent` | ~100ms |
| **Per-test overhead (RestartApp + interactions)** | **~8-15s** |
| **10 tests total** | **~3 minutes** (including one-time setup) |

## Known Gotchas to Handle

1. **Go embed cache**: When testing template changes, `go build` may cache old embedded frontend assets. The consumer must `go clean -cache` or delete the `.so` before rebuild.

2. **First launch after wipe**: The Go runtime's first cold start on a wiped AVD can take 30s+ or even OOM on swiftshader. The library's `AppTimeout` config handles this. Second+ launches are fast.

3. **SystemUI ANR**: Software-GPU emulators occasionally trigger "System UI isn't responding". The library should auto-dismiss this (tap "Wait") if detected during `WaitForText`/`TapOnText`.

4. **CDP connection race**: The WebView's debug socket becomes available slightly after the activity starts. `ConnectWithRetry` handles this.

5. **Permission dialog text varies by API level**: API 30+ uses "While using the app" / "Only this time" / "Don't allow". API 23-29 uses "Allow" / "Deny". The `permissions` package handles both.

6. **`element.click()` doesn't count as a user gesture** in Android WebView for file inputs and permission-requiring APIs. Must use `Input.dispatchMouseEvent` via CDP instead.

7. **`-no-audio` emulator flag**: Without it, emulator may crash or lag. With it, `getUserMedia({audio:true})` will get permission granted but the stream will fail with `NotReadableError`. Tests for audio should skip the stream assertion and just verify the permission was granted.

8. **Multiple emulators**: If the host has multiple devices connected, serial disambiguation is critical. The library uses the serial returned by the emulator it booted.

## Suggested Implementation Order

1. `adb/` — foundation, everything else depends on it
2. `emulator/` — needs adb, enables manual testing of remaining packages
3. `ui/` (parse.go first, then ui.go) — needs adb
4. `cdp/` (ws.go first, then cdp.go) — needs adb for port forwarding
5. `permissions/` — composed from ui, trivial once ui works
6. `testkit.go` — composed from all above, straightforward
7. `examples/` — a real Wails app test demonstrating the full flow

## Non-Goals (explicitly out of scope)

- iOS/Simulator support (different toolchain entirely — future separate library)
- Visual regression testing (screenshot comparison)
- Performance benchmarking
- Network mocking/interception
- Multiple simultaneous emulators (v1 supports one device per test suite)
- Flaky-test retry logic (consumers handle this at the runner level)
