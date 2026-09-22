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

Install the optional diagnostic command once, then check the owned-AVD path on
a workstation or self-hosted runner before booting anything:

```sh
go install github.com/GyldendalDigital/go-adbtest/cmd/adbtest@latest
adbtest doctor
adbtest doctor --avd small_phone_api_35
```

`adbtest doctor` is read-only. It runs sequential, bounded checks for Go, the
Android SDK root, `adb`, the emulator, Command-line Tools, the requested
hardware profile, VM acceleration, configured AVD names, free space for the
AVD's userdata partition, and optional `aapt`.
It does not start an emulator or ADB server, contact a device or package
repository, install packages, accept licences, or change an AVD. Exit code 0
means no required check failed (warnings are allowed), 1 means the environment
is not ready, and 2 means the command or its arguments could not be processed.
Use `--device-profile ID` when provisioning something other than the default
`small_phone` hardware profile.

### Free space for the userdata partition

The emulator refuses to create an AVD's userdata partition unless the
filesystem holding the AVD content directory has **1.2x** the configured
`disk.dataPartition.size` free. It reports the shortfall as `Not enough space
to create userdata partition`, but that goes to the emulator's own output,
which `emulator.Start` writes to stdout rather than attaching to the error.
What a go-adbtest log shows instead is `emulator process exited before its
serial was detected` — but that error now carries the emulator's own account of
what happened; see [When the emulator fails to
start](#when-the-emulator-fails-to-start).

Two figures are worth budgeting for, and they differ by provenance:

| AVD | `disk.dataPartition.size` | Free space needed |
| --- | --- | --- |
| Created by `EnsureAVD` or `avdmanager` | `10G` | **12.0 GiB** |
| No configured size (the emulator's default) | 6 GiB | **7.2 GiB** |
| Configured below 6 GiB, e.g. `2G` | raised to 6 GiB | **7.2 GiB** |

Lowering `disk.dataPartition.size` below 6 GiB does not shrink the requirement:
the emulator raises anything smaller to its own minimum and writes the raised
value back into `config.ini`.

`adbtest doctor --avd NAME` compares that AVD's own configuration against the
free space where its files live. Without `--avd` it reports free space against
the smallest requirement any AVD can have, and never fails, because it cannot
know which AVD will be booted.

The emulator applies this check only when it **creates** the partition, so an
AVD that has already booted keeps working below the threshold and doctor
reports it as a warning rather than a failure. This is why the problem tends to
appear on ephemeral CI runners and never on a workstation.

On macOS the figure is `statfs`'s unprivileged-available space, which is what
`df` reports rather than what Finder shows, since Finder adds purgeable space
and Time Machine local snapshots. Expect doctor's number to be the lower one.
The emulator's own check was measured on Linux, so treat a macOS verdict as
indicative rather than exact.

### When the emulator fails to start

When `Setup` or `emulator.Start` fails before the device is usable, the returned
error carries the emulator's own recent output as well as the exit status:

```
emulator process exited before its serial was detected: exit status 1
last emulator output:
INFO         | Android emulator version 36.6.11.0 (build_id 15507667) (CL:N/A)
INFO         | Graphics backend: gfxstream
INFO         | Increasing RAM size to 2560MB
WARNING      | Feature QuickbootFileBacked is disabled due to stability issues...
FATAL        | Not enough space to create userdata partition. Available: 3156.88 MB at /run/user/1000/ci.avd, need 245760.00 MB.
```

The output is still streamed live to stdout and stderr, on the same streams the
emulator wrote it to. One difference from before: the emulator now writes to a
pipe rather than to whatever the parent's descriptors pointed at, so an
interactive run receives its output in blocks rather than line by line. That
does not affect a failing start — the emulator flushes on exit, and its own
logger issues one write per line once it is initialised — nor CI, where stdout
was already a pipe. What is new is that a copy is retained
and attached to the error, because the live copy scrolls past in a CI log a long
way from the failure a consumer actually handles.

Every hard startup failure is short — 18 lines or fewer — so the tail above is
the whole log. Some failures are not like that. An unsupported `-gpu` value
reports at line 6 of more than a hundred and the emulator carries on booting,
so that line is lifted out and shown first:

```
emulator emulator-5554 did not boot: context deadline exceeded
emulator reported: ERROR        | gpuChoiceBasedOnGpuOptions: Selected GPU option 'bogusmode' is not valid, switching to 'auto' mode.
last emulator output:
...
```

The lifted line is the last `FATAL`, or the **first** error when there is no
fatal. Not the last error: this emulator logs benign ones late in every run —
during graphics init, and again on shutdown — so taking the last would name a
line that had nothing to do with the failure, which is worse than naming
nothing. It is also omitted when it is already the tail's last line, rather than
printed twice.

The adb public key is dropped from the retained copy. The emulator logs it
exactly twice per boot, it carries your user and host name, and it is about a
sixth of a normal startup log.

## Quick start

The root package is `adbtest`. `Setup` is intended for `TestMain`; it validates
the configuration, starts an already-configured AVD or attaches to one device,
installs and launches the APK, and waits for the WebView CDP endpoint. `Setup`
never downloads a system image or creates an AVD.

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
    config := adbtest.HeadlessAVD("small_phone_api_35", "bin/myapp.apk")
    config.AppPackage = "com.example.myapp"
    config.AppTimeout = 2 * time.Minute // Useful for slow Go/WebView startup.
    device = adbtest.Setup(config)

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
    device.CDP.WaitForSelector(t, "#login-btn", 15*time.Second)
    device.CDP.Click(t, "#login-btn")
    device.Permissions.Grant(t, 10*time.Second)

    result := device.CDP.WaitForText(
        t,
        "#status",
        "logged in",
        15*time.Second,
    )
    t.Logf("login status: %s", result)
}
```

## Finding and selecting an AVD

`Config.AVD` is not a device model or a running-device serial. It is the exact
command-line ID of an Android Virtual Device definition that already exists on
the machine. These identifiers are related, but not interchangeable:

| Identifier | Example | How to find it | Where it is used |
| --- | --- | --- | --- |
| AVD ID (command-line name) | `small_phone_api_35` | `emulator -list-avds` | `AVDProfile.Name`, `HeadlessAVD`, or `Config.AVD` |
| Hardware-profile ID | `small_phone` | `avdmanager list device -c` | AVD creation and the CI runner's `profile` input |
| ADB serial | `emulator-5554` | `adb devices -l` | `Config.Serial` for an already-running device |

For example, `Pixel_7` might be a locally chosen AVD ID, while `pixel_7`
is a hardware-profile ID. Always copy the AVD ID from `emulator -list-avds`
when using owned-AVD mode.

### Provision a lightweight AVD explicitly

`EnsureAVD` is the opt-in preparation step for a local machine or persistent
self-hosted runner. `Name` and `APILevel` are required. Its empty profile
fields select the library's lightweight defaults:

| `AVDProfile` field | Empty-value default |
| --- | --- |
| `Device` | `small_phone` |
| `Target` | non-Play `google_apis` |
| `Arch` | `x86_64` on an amd64 host; `arm64-v8a` on Apple Silicon |
| `InstallSystemImage` | `false` |

The provisioning/doctor path supports Linux x86_64 and macOS running a native
amd64 or arm64 Go process. It rejects a cross-architecture image, Play Store
or ATD target, and Windows provisioning for now. Ordinary `Setup` with an
already-configured Windows AVD remains a separate path. Provisioning can
download several gigabytes, so image installation remains an explicit choice:

```go
package androidtest

import (
    "context"
    "fmt"
    "os"
    "time"

    adbtest "github.com/GyldendalDigital/go-adbtest"
)

// Call prepareAVD once from TestMain or a separate bootstrap command.
func prepareAVD() (adbtest.Config, error) {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
    defer cancel()

    avd, err := adbtest.EnsureAVD(ctx, adbtest.AVDProfile{
        Name:               "small_phone_api_35",
        APILevel:           35,
        InstallSystemImage: true, // Only after accepting SDK licences.
        Progress:           os.Stderr,
    })
    if err != nil {
        return adbtest.Config{}, fmt.Errorf("prepare Android test AVD: %w", err)
    }

    config := avd.HeadlessConfig("bin/myapp.apk")
    config.AppPackage = "com.example.myapp"
    return config, nil
}
```

Before enabling `InstallSystemImage`, accept the Android SDK licences yourself
with the interactive command reported by `EnsureAVD` (normally
`sdkmanager --licenses`). With installation disabled, a missing image returns
the exact install and licence commands instead of changing the SDK. A matching
existing AVD is reused; a same-name mismatch is reported and is never
overwritten, deleted, or silently rewritten. `EnsureAVD` never starts an
emulator.

`AVD.HeadlessConfig` composes the result with `HeadlessAVD`. When `Setup` later
starts it, the effective safe profile uses `-no-window`, `-no-boot-anim`,
`-no-audio`, `-no-snapshot`, `-cores 2`, `-gpu auto`, and `-accel on`. It does
not wipe data or force a RAM override. This separation lets provisioning run
once while each test suite owns one bounded, lightweight emulator lifecycle.

`EnsureAVD` and `adbtest doctor` require a stable SDK root in `ANDROID_HOME` or
the legacy `ANDROID_SDK_ROOT`; if both are set, they must resolve to the same
SDK. They do not combine tools from that SDK with similarly named tools on
`PATH`. If `ANDROID_USER_HOME`, `ANDROID_EMULATOR_HOME`, or legacy
`ANDROID_SDK_HOME` relocates Android state, also set `ANDROID_AVD_HOME` to the
one AVD directory that both `avdmanager` and the emulator should use. An
implicit relocated home is rejected rather than risking creation in one
directory and launch from another.

Calls in one process are serialized per SDK root. Run provisioning as one
bootstrap step rather than from parallel processes that share a writable SDK
or AVD home; run the test packages serially afterward with `go test -p=1`.

### Find or create an AVD in Android Studio

Open Android Studio's
[Device Manager](https://developer.android.com/studio/run/managing-avds) from
either location:

- Welcome screen: **More Actions → Virtual Device Manager**
- Open project: **View → Tool Windows → Device Manager**

The **Virtual** tab lists the configured devices. Its friendly display name can
differ from the command-line ID. Use the device's edit button, open **Show
Advanced Settings**, and find **AVD ID**. Alternatively,
[`emulator -list-avds`](https://developer.android.com/studio/run/emulator-commandline)
prints the authoritative, copyable IDs accepted by this library. When an
emulator is already running in a separate window, its title usually shows the
display name and console port. Given its ADB serial, query the AVD ID directly
with:

```sh
adb -s emulator-5554 emu avd name
```

To create a lightweight test device in Device Manager, select **Create Virtual
Device**, choose the **Small Phone** hardware profile, and select a non-Play
Store **Google APIs** image whose ABI matches the host. Use `x86_64` on
x86-64 machines and GitHub's Ubuntu runners; use `arm64-v8a` on Apple Silicon.
The explicit `EnsureAVD` path currently supports Linux x86_64 and macOS
amd64/arm64, not Windows provisioning. Give the AVD a stable ID such as
`small_phone_api_35`. A Play Store image is unnecessary unless the application
specifically tests Play Store behavior.

### Find or create an AVD from the command line

List existing AVDs and available hardware profiles:

```sh
emulator -list-avds
avdmanager list avd -c
avdmanager list device -c
```

If those tools are not on `PATH`, invoke them below `ANDROID_HOME` or
`ANDROID_SDK_ROOT`. For example, use `$ANDROID_HOME/emulator/emulator`,
`$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager`, and
`$ANDROID_HOME/cmdline-tools/latest/bin/avdmanager` (or replace
`ANDROID_HOME` with `ANDROID_SDK_ROOT`).

The equivalent one-time creation flow on an x86_64 host is:

```sh
sdkmanager --licenses # Interactive, once per SDK installation/update.
sdkmanager 'system-images;android-35;google_apis;x86_64'
echo no | avdmanager create avd \
  --name small_phone_api_35 \
  --package 'system-images;android-35;google_apis;x86_64' \
  --device small_phone

emulator -list-avds
emulator -accel-check
```

Pass one of the exact listed names to the library:

```go
config := adbtest.HeadlessAVD("small_phone_api_35", "bin/myapp.apk")
config.AppPackage = "com.example.myapp"
device = adbtest.Setup(config)
```

`HeadlessAVD` and `Setup` apply launch-time behavior only; neither downloads a
system image, creates an AVD, or permanently rewrites its Device Manager
settings. Use the separate `EnsureAVD` call when explicit provisioning is
wanted. If a developer has already started the AVD manually, use its ADB
`Serial` instead of asking the library to start a second instance. The root
library does not read `ADBTEST_AVD` or `ADBTEST_APK` automatically; applications
that use environment variables must pass their values into `Config` themselves.

## Setup modes and defaults

Exactly one device mode is required:

- `AVD` starts an existing configured emulator owned by the `Device`.
  `Teardown` stops it. `Setup` does not download an image or create the AVD;
  call `EnsureAVD` separately when provisioning is desired.
- `Serial` attaches to an already-running emulator or device. `Teardown`
  closes CDP and force-stops the launched app, but does not stop that device.

Selection is always explicit; `Setup` never guesses among connected devices.
AVD-only fields (`Headless`, `GPU`, `Cores`, `MemoryMB`, `Acceleration`,
`NoAudio`, `WipeData`, and `NoSnapshot`) must not be set with `Serial`.

The zero value of every boolean remains `false`. Set `Headless: true` and
`NoAudio: true` explicitly when desired. Other zero values receive these
defaults:

| Field | Default |
| --- | --- |
| `GPU` | `auto` in AVD mode |
| `Cores` | AVD setting |
| `MemoryMB` | AVD/system-image setting |
| `Acceleration` | Emulator default |
| `BootTimeout` | 120 seconds |
| `AppTimeout` | 30 seconds |
| `CDPPort` | 9222 |

`HeadlessAVD` wraps the conservative owned-emulator profile: headless, GPU
auto-selection, two virtual CPUs, required VM acceleration, no audio, no
snapshot load/save, and no data wipe. Requiring acceleration makes an
unsuitable host fail fast instead of falling back to expensive CPU emulation.
It leaves RAM to the AVD/system image because
[current Android phone images can enforce a higher safe minimum](https://android.googlesource.com/platform/external/qemu/+/emu-master-dev/android/android-emu/android/main-common.c#1250)
than a command-line override.
`NoSnapshot` forces a cold boot and prevents a failed run from saving unstable
quick-boot state.

For the lowest practical footprint, create the AVD itself with a small phone
profile (for example 720×1280), a non-Play-Store `google_apis` image with the
host-native ABI, and hardware virtualization. Software graphics move rendering
work onto the CPU, so `GPU: "auto"` is the best general default; use current
`GPU: "swiftshader"` only when a graphics-less runner needs deterministic
software rendering. `swiftshader_indirect` is deprecated. `MemoryMB` is an
advanced 1536–8192 MB override, not a guaranteed host-memory ceiling.
[Android recommends GPU auto-selection and VM acceleration](https://developer.android.com/studio/run/emulator-acceleration).
The same `sdkmanager`/`avdmanager` recipe is also included in the
[integration example](examples/README.md#device-modes).

Run one owned emulator per suite. If Android tests span several Go packages,
use `go test -p=1` so package-level `TestMain` functions cannot boot AVDs in
parallel. [Automated Test Device images](https://developer.android.com/studio/test/managed-devices#use-atd)
can be smaller, but remove components such as SystemUI and Settings; that makes
them unsuitable as the default for permission-dialog and DocumentsUI tests.

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
contexts rather than fixed sleeps. Activity Manager's `-W` initial-display
timeout is advisory after Android accepts a launch; `Setup` and `RestartApp`
use CDP connection/reconnection as the readiness proof. CDP forwarding is
exclusive: `Setup` fails
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

## Using the device APIs

`Setup` returns one `Device` that exposes the Android and WebView interaction
surfaces used by a test:

| API | Target | Typical values |
| --- | --- | --- |
| `device.CDP` | DOM inside the debuggable WebView | CSS selectors and JavaScript expressions |
| `device.UI` | Native Android and system UI | Visible text, Android resource IDs, or screen coordinates |
| `device.Permissions` | Android runtime-permission dialogs | Grant, deny, or limited-media choices |
| `device.ADB` | Lower-level device operations | Shell commands, files, screenshots, and forwarding |

The high-level CDP, UI, and permission helpers accept `testing.TB` and fail the
current test with a descriptive error. CDP evaluation, native UI, and ADB also
expose error-returning or context-aware primitives when a test needs lower-level
control.

### WebView interaction with `device.CDP`

CDP selectors are CSS selectors evaluated in the current WebView document.
They are not Android resource IDs. Prefer stable attributes such as
`data-testid` over presentation-oriented classes:

```go
func TestCheckout(t *testing.T) {
    const timeout = 15 * time.Second
    const checkoutButton = `[data-testid="checkout"]`

    device.RestartApp(t)
    device.CDP.WaitForSelector(t, checkoutButton, timeout)
    device.CDP.Click(t, checkoutButton)

    device.CDP.WaitForText(
        t,
        `[data-testid="status"]`,
        "Order complete",
        timeout,
    )
}
```

`WaitForSelector` polls until the first matching element has non-zero bounds
and is not hidden by its own or an ancestor's display, visibility, or opacity.
`Click(t, selector)` itself does not wait: `selector` is a CSS selector such as
`#login-btn` or `[data-testid="checkout"]`, not an Android resource ID. The
helper uses `document.querySelector`, reads the first match's
`getBoundingClientRect`, and sends CDP `mousePressed` and `mouseReleased` events
at its center. This is a real input gesture rather than JavaScript
`element.click()`, which matters for browser-gated actions such as file inputs.
It fails the test when the selector is invalid or absent. Call
`WaitForSelector` first when the element may render asynchronously.

`WaitForText` polls the first matching element until its `textContent` contains
the requested case-sensitive substring, then returns the complete text. It
does not require that element to be visible, so combine it with
`WaitForSelector` when visibility is part of the assertion.

Evaluate JavaScript directly when a semantic helper is not enough:

```go
title := device.CDP.Eval(t, `document.title`)
countJSON := device.CDP.Eval(t, `document.querySelectorAll(".result").length`)
profileJSON := device.CDP.Eval(t, `({name: "Ada", active: true})`)
ready := device.CDP.EvalAsync(t, `Promise.resolve("ready")`)

t.Log(title, countJSON, profileJSON, ready)
```

`Eval` returns JavaScript strings directly, serializable non-string values as
JSON text, and special values such as `undefined` or `NaN` in CDP notation.
`EvalAsync` additionally waits for a returned Promise. JavaScript exceptions,
invalid selectors, protocol errors, missing elements in `Click`, and expired
timeouts fail the test. Use `EvalE` or `EvalContext` when the caller needs an
error instead of `t.Fatalf`.

### Native Android interaction with `device.UI`

Native helpers inspect the `uiautomator` hierarchy. Text matching is a
case-sensitive substring; resource ID matching is exact and normally includes
the package prefix:

```go
device.UI.WaitForText(t, "Choose a file", 10*time.Second)
device.UI.TapOnText(t, "Downloads", 10*time.Second)
device.UI.LongPressOnText(t, "report.pdf", 10*time.Second)
device.UI.TapOnID(
    t,
    "com.example.myapp:id/submit",
    10*time.Second,
)

device.UI.AssertVisible(t, "Upload complete")
device.UI.AssertGone(t, "Loading")

if err := device.UI.TypeText("hello world"); err != nil {
    t.Fatal(err)
}
```

The wait and tap helpers use the interactor's ten-second default when no
positive timeout is supplied. `TapOnText` prefers a clickable match;
`TapOnID` selects the first exact resource-ID match. `AssertVisible` and
`AssertGone` check text presence in one current hierarchy dump—they do not
poll or independently verify screen bounds—so use `WaitForText` first for
asynchronous UI. Inspect `Dump()` when you need the exact package-qualified ID
used by the current Android image. `Dump`, coordinate `Tap`, and their context
variants are available as error-returning primitives.

### Runtime permissions with `device.Permissions`

Trigger the permission request in the application first, then handle the
native Android dialog:

```go
device.CDP.WaitForSelector(t, "#request-permissions", 10*time.Second)
device.CDP.Click(t, "#request-permissions")
device.Permissions.GrantAll(t, 15*time.Second)

// Alternatives for one current dialog:
// device.Permissions.Grant(t)
// device.Permissions.Deny(t)
// device.Permissions.GrantSelected(t) // Caller completes the system picker.
```

`Grant` prefers the most complete recognized access choice. `GrantAll` handles
up to five sequential dialogs under one total timeout. Dialog matching is
restricted to known Android permission-controller and package-installer
packages, so application-owned text such as “Allow” is ignored. The default
timeout is ten seconds. `IsVisible` is observational and returns false if the
hierarchy cannot be read; do not use it as the sole test assertion.

### Application lifecycle and lower-level ADB

`RestartApp(t)` is the normal per-test reset. It force-stops the app, launches
it again, rediscovers the WebView process, and reconnects CDP while retaining
application data and granted permissions.

`ForceStop` only stops the app, while `LaunchApp` only requests a launch. A
manual stop/start sequence must reconnect CDP explicitly:

```go
if err := device.ForceStop(); err != nil {
    t.Fatal(err)
}
device.LaunchApp(t)
device.CDP.Reconnect(t)
device.CDP.WaitForSelector(t, "html", 15*time.Second)
```

Prefer `RestartApp` unless the stopped interval itself is under test. For
operations without a dedicated helper, use the serial-pinned ADB client:

```go
androidVersion := device.ADB.ShellOrFail(t, "getprop ro.build.version.release")
if err := device.ADB.Screencap("failure.png"); err != nil {
    t.Fatal(err)
}
t.Logf("Android %s", androidVersion)
```

## Features

- Native UI interaction by text or resource ID through `uiautomator`
- WebView evaluation, visibility waits, and real-gesture clicks through CDP
- Runtime permission grant/deny handling across Android permission-controller
  variants
- Explicit, non-overwriting lightweight AVD provisioning with opt-in downloads
- Read-only environment diagnostics through `adbtest doctor`
- Owned AVD lifecycle or explicit attachment to an existing device
- Context-aware ADB and CDP primitives for lower-level composition
- Cleanup of WebSocket connections, ADB forwards, app processes, and owned
  emulator processes

Each subpackage is independently usable:

```text
go-adbtest/
├── testkit.go          # root package adbtest: Config, Device, Setup
├── avd.go              # explicit lightweight AVD provisioning
├── adb/                # context-aware ADB commands and device discovery
├── emulator/           # emulator start, boot monitoring, and shutdown
├── ui/                 # native UI parsing and interaction
├── cdp/                # WebView CDP transport and high-level actions
├── permissions/        # Android runtime-permission dialogs
└── cmd/adbtest/        # read-only environment doctor
```

## Prerequisites

- Go 1.23 or later
- Android SDK platform-tools (`adb`)
- For AVD mode: the Android emulator binary and a configured AVD, prepared
  manually or with the separate `EnsureAVD` API
- For `EnsureAVD`: Android SDK Command-line Tools (`sdkmanager` and
  `avdmanager`) plus `ANDROID_HOME` or `ANDROID_SDK_ROOT`, on Linux x86_64 or
  native macOS amd64/arm64
- For package auto-detection: Android build-tools (`aapt`)
- An application WebView that exposes remote debugging; see below

### Preparing a CDP-ready APK

`device.CDP` requires the running WebView to expose a DevTools/CDP endpoint.
The normal, safe test artifact is a development/debug APK. The Android Gradle
Plugin's standard `debug` build type sets `android:debuggable="true"` in the
merged manifest, and WebView 113 or later enables WebView debugging
automatically for such applications. For older WebView providers or a custom
Android host, enable it explicitly only in debug builds:

```java
if (BuildConfig.DEBUG) {
    WebView.setWebContentsDebuggingEnabled(true);
}
```

Framework scaffolds often configure this already. Wails v3 Android scaffolding
normally produces a CDP-ready generated debug APK, so try that artifact before
changing Wails or generated Android sources. If CDP connection still fails,
inspect the app's local Gradle and activity sources because the
generated host is customizable and version-specific. Do not enable WebView
debugging in a production build because an ADB user could inspect and modify
its WebView contents. See Android's documentation for
[build variants](https://developer.android.com/build/build-variants) and
[`setWebContentsDebuggingEnabled`](https://developer.android.com/reference/android/webkit/WebView#setWebContentsDebuggingEnabled(boolean)).

Optionally check the packaged APK's manifest setting with Android SDK
[APK Analyzer](https://developer.android.com/tools/apkanalyzer):

```sh
apkanalyzer manifest debuggable path/to/app-debug.apk
```

For the conventional debug-build path, this should print `true`. It checks only
the packaged manifest and is not independently a go-adbtest requirement; the
runtime requirement is a reachable CDP endpoint. A successful go-adbtest CDP
connection is that runtime proof. A failed connection can also mean that the
configured process is wrong, the WebView has not started, or ADB forwarding
failed. `adbtest doctor` remains a read-only host-environment check and
intentionally does not install, launch, or inspect the application.

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
go test -p=1 -tags android_integration -timeout 5m ./...
```

## CI with an attached emulator

GitHub-hosted runners do not have one of your workstation's AVDs to select.
[`reactivecircus/android-emulator-runner`](https://github.com/ReactiveCircus/android-emulator-runner)
installs the requested image, creates an ephemeral AVD, boots it, runs the
script, and stops it. Its relevant inputs have distinct roles:

| Runner input | Selects |
| --- | --- |
| `api-level`, `target`, `arch` | Android system image |
| `profile` | Hardware-profile ID, such as `small_phone` |
| `avd-name` | Name assigned to the CI-created AVD |
| `emulator-port` | Console port and therefore ADB serial, such as `emulator-5554` |
| `cores` | Virtual CPU count |

Because the runner owns the emulator lifecycle, the Go test must attach with
`Serial`; using `AVD` or `HeadlessAVD` here would attempt to start a second
emulator. The action exposes `EMULATOR_PORT` inside its script, so the serial
can be selected deterministically instead of taking the first result from
`adb devices`:

Setting `emulator-options` replaces the action's complete default option list,
which is why the example repeats every required headless/safety option. If the
port is customized, choose an unused even console port so the emulator remains
discoverable by ADB.

```yaml
jobs:
  android-integration:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version: '1.23.x'
      - name: Enable KVM
        run: |
          echo 'KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"' \
            | sudo tee /etc/udev/rules.d/99-kvm4all.rules
          sudo udevadm control --reload-rules
          sudo udevadm trigger --name-match=kvm
      - uses: reactivecircus/android-emulator-runner@v2
        with:
          api-level: 35
          arch: x86_64
          target: google_apis
          profile: small_phone
          avd-name: go_adbtest_api_35
          emulator-port: 5554
          cores: 2
          emulator-options: >-
            -no-window -gpu auto -accel on -no-audio -no-boot-anim -no-snapshot
          script: |
            ./your-android-build-command
            export ADBTEST_APK="$PWD/path/to/app.apk"
            export ADBTEST_SERIAL="emulator-${EMULATOR_PORT}"
            adb -s "$ADBTEST_SERIAL" get-state
            go test -p=1 -tags android_integration -timeout 5m ./...
```

This workflow assumes `TestMain` uses the attached-device configuration shown
above and reads `ADBTEST_SERIAL`. Exporting the variable does not alter a
hard-coded `HeadlessAVD` call.

`Setup` installs `ADBTEST_APK`; the CI build step should build it but need not
run a separate `adb install`. Installation uses `adb install -r`, so an existing
installation's application data and granted permissions are retained. A
complete, copyable consumer test and workflow is available in
[examples](examples/README.md).

On a persistent self-hosted runner, the library can own an AVD instead. Create
it once under a stable name—manually or through a separately bounded
`EnsureAVD` bootstrap step—then check prerequisite health and exact-name
presence with `adbtest doctor --avd NAME` and pass that name to `HeadlessAVD`.
Doctor reads that AVD's `config.ini` to size its userdata partition, but does
not validate its image or lightweight profile; `EnsureAVD` does that. Keep the
large system-image download out of the normal test hot path
when possible. Do not manually start that same AVD first. In either CI mode,
keep `go test -p=1` so multiple package-level `TestMain` functions do not
launch emulators concurrently.

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
