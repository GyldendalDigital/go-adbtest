# go-adbtest Implementation Specification

This document describes the implemented contract of `go-adbtest`. The library
drives one Android WebView application from Go tests, crossing between native
Android UI and WebView content without relying on hard-coded screen
coordinates.

The module is:

```text
github.com/GyldendalDigital/go-adbtest
```

The root package name is `adbtest`.

## Intended use

```go
package androidtest

import (
    "fmt"
    "os"
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
        NoAudio:    true,
    })

    code := m.Run()
    if err := device.Teardown(); err != nil {
        fmt.Fprintln(os.Stderr, err)
        if code == 0 {
            code = 1
        }
    }
    os.Exit(code)
}

func TestWebViewFlow(t *testing.T) {
    device.RestartApp(t)
    device.CDP.Click(t, "#continue")
    device.Permissions.Grant(t)
    device.CDP.WaitForSelector(t, "#result", 10*time.Second)
}
```

`go-adbtest` is not Wails-specific. The application only needs a debuggable
WebView with WebView debugging enabled.

## Architecture

```text
go-adbtest/
├── testkit.go          # package adbtest: Config, Device, Setup
├── adb/                # context-aware adb process wrapper
├── emulator/           # owned emulator lifecycle
├── ui/                 # uiautomator parsing and native interaction
├── cdp/                # CDP WebSocket transport and WebView interaction
└── permissions/        # Android runtime-permission dialogs
```

Each subpackage is independently usable. The root package composes them into a
single `Device`.

## Root `adbtest` package

### Configuration

```go
type Config struct {
    AVD         string
    Serial      string
    APK         string
    AppPackage  string
    AppActivity string
    Headless    bool
    GPU         string
    NoAudio     bool
    WipeData    bool
    BootTimeout time.Duration
    AppTimeout  time.Duration
    CDPPort     int
}
```

Exactly one of `AVD` and `Serial` is required:

- `AVD` starts a new emulator. The returned `Device` owns and stops it.
- `Serial` attaches to an existing emulator or physical device. The returned
  `Device` never stops that device, and `Device.Emulator` is `nil`.

The library never implicitly chooses the first connected device. `Headless`,
`GPU`, `NoAudio`, and `WipeData` apply only in AVD mode and are rejected in
attached mode.

Boolean fields retain normal Go zero-value semantics: `false` remains false.
There are no hidden `true` defaults. The non-boolean zero-value defaults are:

| Field | Default |
| --- | --- |
| `GPU` | `swiftshader_indirect` in AVD mode |
| `BootTimeout` | 120 seconds |
| `AppTimeout` | 30 seconds |
| `CDPPort` | 9222 |

`APK` must name a regular file. If `AppPackage` is empty, Setup runs
`aapt dump badging` before acquiring a device. AAPT resolution is:

1. the `AAPT` environment variable;
2. `aapt` on `PATH`;
3. the newest executable `aapt` under `ANDROID_HOME/build-tools` or
   `ANDROID_SDK_ROOT/build-tools`.

If `AppPackage` is supplied, `aapt` is not required. APK inspection also keeps
the first `launchable-activity` when present.

`AppActivity` may be a short class, dot-prefixed class, fully qualified class,
or a component whose package matches `AppPackage`. When it is empty, Setup
uses APK metadata when available, then tries these device commands:

```text
cmd package resolve-activity --brief \
    -a android.intent.action.MAIN \
    -c android.intent.category.LAUNCHER <package>

pm resolve-activity --brief \
    -a android.intent.action.MAIN \
    -c android.intent.category.LAUNCHER <package>
```

### Device API

```go
type Device struct {
    Emulator    *emulator.Instance
    ADB         *adb.Client
    UI          *ui.Interactor
    CDP         *cdp.Client
    Permissions *permissions.Handler
    Config      Config
}

func Setup(cfg Config) *Device
func (d *Device) Teardown() error
func (d *Device) ForceStop() error
func (d *Device) LaunchApp(t testing.TB)
func (d *Device) RestartApp(t testing.TB)
```

`Config` on the returned device contains normalized defaults and the resolved
package/activity.

`Setup` is a `TestMain` convenience and panics on failure. It performs these
operations:

1. Normalize and validate configuration and the APK.
2. Inspect the APK when package auto-detection is needed.
3. Start an owned AVD, or attach to `Serial` and wait for device/boot readiness.
4. Construct native UI and permission helpers.
5. Install the APK with `adb install -r`.
6. Resolve the launcher activity and run `am start -W -n <component>`.
7. Poll app PID, WebView socket forwarding, target discovery, and WebSocket
   connection until CDP is ready.

Setup uses bounded contexts and no fixed app-start sleeps. Launch and initial
CDP readiness share one `AppTimeout` context. If any step fails, resources
already acquired are cleaned before Setup panics.

`LaunchApp` launches the resolved component but does not reconnect CDP.
`RestartApp` force-stops, launches, and reconnects CDP within `AppTimeout`.
Force-stopping does not clear application data.

`Teardown` is nil-safe and idempotent. It closes CDP/removes its ADB forward,
force-stops an app launched by the device, and kills an owned emulator. It
continues after individual cleanup failures and returns their joined error. An
attached emulator/device remains running.

## `adb` package

```go
type Client struct {
    Serial  string
    ADBPath string
}

func New(serial string) (*Client, error)

func (c *Client) Run(args ...string) (string, error)
func (c *Client) RunContext(ctx context.Context, args ...string) (string, error)
func (c *Client) Shell(command string) (string, error)
func (c *Client) ShellContext(ctx context.Context, command string) (string, error)
func (c *Client) ShellOrFail(t testing.TB, command string) string
func (c *Client) Install(apkPath string) error
func (c *Client) Push(local, remote string) error
func (c *Client) Pull(remote, local string) error
func (c *Client) Forward(localPort int, abstractSocket string) error
func (c *Client) RemoveForward(localPort int) error
func (c *Client) Screencap(localPath string) error
func (c *Client) WaitForDevice(timeout time.Duration) error
func (c *Client) WaitForDeviceContext(ctx context.Context) error

func Devices() ([]string, error)
func DevicesContext(ctx context.Context) ([]string, error)
```

`Run` executes host-side ADB subcommands. `Shell` supplies one command string to
`adb shell`. Both return trimmed combined output. Context variants terminate
and reap the ADB child process on cancellation. `-s <Serial>` is inserted
before the host command when a serial is configured.

ADB executable resolution is:

1. `$ANDROID_HOME/platform-tools/adb`;
2. `$ANDROID_SDK_ROOT/platform-tools/adb`;
3. `adb` on `PATH`.

`Devices` returns only entries whose state is exactly `device`; offline and
unauthorized entries are omitted.

## `emulator` package

```go
type Config struct {
    AVD        string
    Headless   bool
    GPU        string
    NoAudio    bool
    WipeData   bool
    NoSnapshot bool
    Timeout    time.Duration
}

type Instance struct {
    PID    int
    Serial string
    ADB    *adb.Client
}

func Start(cfg Config) (*Instance, error)
func (i *Instance) WaitForBoot(timeout time.Duration) error
func (i *Instance) Kill() error
func (i *Instance) IsRunning() bool
```

`AVD` is required. A zero timeout becomes 120 seconds; a negative timeout is
rejected. Boolean fields remain false unless explicitly enabled.

Start first obtains a checked baseline from `adb devices`, launches the
emulator, and detects the new serial by comparing device lists. Serial
detection and `sys.boot_completed` polling share one overall timeout. The
child process is monitored concurrently so an early exit interrupts a blocked
ADB operation and is reported directly. Every post-start failure kills and
reaps the child.

`Kill` is nil-safe and idempotent. It runs the host-side command
`adb -s <serial> emu kill`, waits up to ten seconds for graceful exit, then
falls back to killing and reaping the process.

## `ui` package

```go
type Element struct {
    Text        string
    ResourceID  string
    ContentDesc string
    Class       string
    Package     string
    Clickable   bool
    Bounds      Rect
}

type Rect struct {
    X1, Y1, X2, Y2 int
}

func (r Rect) CenterX() int
func (r Rect) CenterY() int
func ParseDump(xmlData []byte) ([]Element, error)
func FindByText(elements []Element, text string) []Element
func FindByResourceID(elements []Element, id string) []Element
func FindByTextRegex(elements []Element, pattern string) ([]Element, error)

type Interactor struct {
    ADB     *adb.Client
    Timeout time.Duration
}

func NewInteractor(adbClient *adb.Client) *Interactor
func (u *Interactor) TapOnText(t testing.TB, text string, timeout ...time.Duration)
func (u *Interactor) LongPressOnText(t testing.TB, text string, timeout ...time.Duration)
func (u *Interactor) TapOnID(t testing.TB, resourceID string, timeout ...time.Duration)
func (u *Interactor) WaitForText(t testing.TB, text string, timeout ...time.Duration)
func (u *Interactor) AssertVisible(t testing.TB, text string)
func (u *Interactor) AssertGone(t testing.TB, text string)
func (u *Interactor) Dump() ([]Element, error)
func (u *Interactor) Tap(x, y int) error
func (u *Interactor) TypeText(text string) error
```

`ParseDump` flattens the uiautomator XML tree and validates every bounds value.
Text searches are case-sensitive substring searches; resource IDs are exact;
invalid regular expressions return an error.

`Dump` first runs `uiautomator dump /dev/tty` and extracts only the hierarchy
XML. If that fails or is malformed, it dumps to `/sdcard/ui.xml`, pulls the
file into a host temporary directory, parses it, and removes both temporary
files.

Wait/tap helpers poll every 500 ms. An optional positive timeout overrides the
interactor default of ten seconds. Text taps prefer a clickable match and
otherwise use the first match. Native input errors fail the supplied test.
`TypeText` quotes spaces and shell metacharacters as one Android-shell word.
Polling also detects and dismisses the known System UI ANR dialog by tapping
`Wait`.

## `permissions` package

```go
type Handler struct {
    UI *ui.Interactor
}

func NewHandler(interactor *ui.Interactor) *Handler
func (h *Handler) Grant(t testing.TB, timeout ...time.Duration)
func (h *Handler) Deny(t testing.TB, timeout ...time.Duration)
func (h *Handler) GrantAll(t testing.TB, timeout ...time.Duration)
func (h *Handler) IsVisible() bool
```

The handler recognizes only buttons owned by known Android permission
controller/package-installer packages, preventing application text such as
`Allow` from being tapped accidentally. It prefers resource IDs, then exact
English button text, in this grant order:

1. `While using the app`;
2. `Only this time`;
3. `Allow`/`ALLOW`.

Deny recognizes `Don't allow` (including the typographic apostrophe),
`Deny`/`DENY`, and the controller resource ID for deny-and-don't-ask-again.

Dump and tap errors are retried until the timeout. `GrantAll` waits for the
first dialog to appear, drains up to five sequential dialogs, and fails if a
sixth remains. Its transition delay is one second. `IsVisible` returns false
when the hierarchy cannot be inspected.

## `cdp` package

### Transport

```go
func Connect(localPort int) (*Conn, error)
func ConnectContext(ctx context.Context, localPort int) (*Conn, error)
func ConnectWithRetry(localPort int, timeout time.Duration) (*Conn, error)
func ConnectWithRetryContext(ctx context.Context, localPort int) (*Conn, error)
func ConnectDirect(webSocketURL string) (*Conn, error)
func ConnectDirectContext(ctx context.Context, webSocketURL string) (*Conn, error)

func (c *Conn) Send(method string, params map[string]any) (json.RawMessage, error)
func (c *Conn) SendWithTimeout(method string, params map[string]any, timeout time.Duration) (json.RawMessage, error)
func (c *Conn) SendContext(ctx context.Context, method string, params map[string]any) (json.RawMessage, error)
func (c *Conn) Close() error
```

Connection discovery requests `http://localhost:<port>/json/list`, preferring
the first `page` target and otherwise the first target with a WebSocket URL.
The HTTP response must be successful and is limited to 4 MiB.

Every CDP command receives a positive atomic ID. A background reader routes
responses to pending commands. Protocol errors, malformed messages, write
failures, connection loss, and explicit closure are propagated to all affected
waiters. `Close` is safe to call concurrently and repeatedly.

### High-level WebView client

```go
type Client struct {
    Conn       *Conn
    ADB        *adb.Client
    LocalPort  int
    AppPackage string
}

func NewClient(adbClient *adb.Client, appPackage string, localPort int) (*Client, error)
func NewClientContext(ctx context.Context, adbClient *adb.Client, appPackage string, localPort int) (*Client, error)
func (c *Client) Eval(t testing.TB, expression string) string
func (c *Client) EvalE(expression string) (string, error)
func (c *Client) EvalAsync(t testing.TB, expression string) string
func (c *Client) Click(t testing.TB, cssSelector string)
func (c *Client) WaitForSelector(t testing.TB, cssSelector string, timeout time.Duration)
func (c *Client) WaitForText(t testing.TB, cssSelector, text string, timeout time.Duration) string
func (c *Client) Reconnect(t testing.TB)
func (c *Client) ReconnectContext(ctx context.Context) error
func (c *Client) Close() error
func (c *Client) CloseContext(ctx context.Context) error
```

Client creation and reconnection poll for the app PID, forward
`tcp:<port>` to `localabstract:webview_devtools_remote_<PID>`, discover a
target, and dial it under one context. Failed connection attempts remove any
forward they created. Reconnection immediately closes the old connection and
removes the old forward before discovering the new PID.

`Eval` uses `Runtime.evaluate` with `returnByValue`; `EvalAsync` additionally
sets `awaitPromise`. JavaScript exceptions are returned or fail the supplied
test. Non-string values are returned as their JSON representation.

`WaitForSelector` requires an element with non-zero bounds whose ancestor
styles do not hide it. `WaitForText` returns the last matching text content.
`Click` evaluates the element bounds and sends `mousePressed` plus
`mouseReleased` through `Input.dispatchMouseEvent`. This provides a real user
gesture; JavaScript `element.click()` is not a substitute for permission- or
file-input flows.

The non-context constructors use bounded defaults: ten seconds for a raw
connection, 30 seconds for client readiness/reconnection, and two seconds for
client cleanup.

## Testing and integration

The library's unit suite uses deterministic fakes and local HTTP/WebSocket
servers. It requires no Android SDK or running emulator:

```sh
go test ./...
```

Consumer tests requiring Android should use:

```go
//go:build android_integration
```

```sh
go test -tags android_integration -timeout 5m ./...
```

When an external runner already owns an emulator, configure root Setup with
its explicit `Serial`; do not also configure `AVD`. Setup installs the APK, so
the runner script only needs to build it and expose its path and device serial.
See the attached-emulator workflow in the README.

The minimum Go version is 1.23. The module declares that baseline, and the
required CI test job runs against the Go 1.23 release line.

## Dependencies

The only runtime dependency is `nhooyr.io/websocket`. Consumers choose their
own assertion library; `testify` is not a module dependency.

## Operational notes

- Android shell output is trimmed, including CRLF output.
- A wiped AVD and a cold WebView may take significantly longer to initialize;
  configure timeouts rather than adding sleeps.
- `NoAudio: true` improves emulator stability but prevents successful audio
  capture even when the Android permission is granted.
- CDP clicks use CSS pixels; no device-pixel-ratio multiplication is needed.
- Permission labels are currently matched in English. Use a controlled emulator
  locale for these helpers.

## Non-goals for v1

- iOS or Simulator support
- Visual regression testing
- Performance benchmarking
- Network mocking or interception
- Multiple simultaneous devices per `Device`
- Automatic flaky-test retries
