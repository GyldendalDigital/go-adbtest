# Android WebView integration example

This directory contains a generic consumer template for testing an Android app
whose UI is rendered in a debuggable WebView. The Go test demonstrates app
restart, Chrome DevTools Protocol (CDP) selector and text interaction, and
granting Android runtime permissions.

The example is guarded by the `android_integration` build tag. It is therefore
excluded from ordinary `go test ./...` runs. Even with the tag enabled, it safely
skips the device test when its required fixture values or APK are absent.

## Copy it into a consumer project

Add this module to the consumer project, choosing the version you want to pin:

```sh
go get github.com/GyldendalDigital/go-adbtest@latest
```

Copy the Go test into a suitable integration-test package and adjust its package
name if needed:

```sh
mkdir -p integration
cp examples/android_webview_test.go integration/android_webview_test.go
```

To use the CI template, copy it into the consumer repository and replace its
placeholder APK build command, APK path, application identity, selectors, and
expected text:

```sh
mkdir -p .github/workflows
cp examples/android-integration.yml .github/workflows/android-integration.yml
```

The workflow targets `./integration/...`, matching the copy command above.
Change that package path if you place the test elsewhere.

## Required fixture environment

| Variable | Meaning |
| --- | --- |
| `ADBTEST_APK` | Path to the APK under test. An absolute path is recommended. |
| `ADBTEST_AVD` or `ADBTEST_SERIAL` | Exactly one device mode; see below. |
| `ADBTEST_PERMISSION_TRIGGER_SELECTOR` | CSS selector for a WebView control that requests an Android runtime permission. |
| `ADBTEST_RESULT_SELECTOR` | CSS selector whose text reports the result. |
| `ADBTEST_EXPECTED_TEXT` | Text expected within the result element. |

Optional settings are:

| Variable | Default | Meaning |
| --- | --- | --- |
| `ADBTEST_APP_PACKAGE` | Read from the APK | Android application package. Setting it explicitly is recommended in CI. |
| `ADBTEST_APP_ACTIVITY` | Resolved from the APK | Launch activity. Setting it explicitly is recommended in CI. |
| `ADBTEST_INTERACTION_TIMEOUT` | `15s` | Timeout for selector, text, and permission interaction. |
| `ADBTEST_BOOT_TIMEOUT` | Library default | Emulator/device boot timeout. |
| `ADBTEST_APP_TIMEOUT` | Library default | Application/CDP startup timeout. |
| `ADBTEST_CDP_PORT` | Library default | Local CDP forwarding port. |
| `ADBTEST_GPU` | Library default | AVD GPU mode; valid only with `ADBTEST_AVD`. |
| `ADBTEST_HEADLESS` | `false` | Start an owned AVD without a window. |
| `ADBTEST_NO_AUDIO` | `false` | Disable audio for an owned AVD. |
| `ADBTEST_WIPE_DATA` | `false` | Wipe an owned AVD before starting it. |

The trigger fixture must actually cause the application to request a runtime
permission. `GrantAll` safely returns when no permission dialog appears, but in
that case the example is no longer exercising the intended permission flow.

## Device modes

In AVD mode, go-adbtest starts, owns, and stops the emulator:

```sh
ADBTEST_APK="$PWD/app/build/outputs/apk/debug/app-debug.apk" \
ADBTEST_AVD=Pixel_API_35 \
ADBTEST_HEADLESS=true \
ADBTEST_PERMISSION_TRIGGER_SELECTOR='#request-permission' \
ADBTEST_RESULT_SELECTOR='#permission-status' \
ADBTEST_EXPECTED_TEXT=granted \
go test -tags=android_integration -count=1 -timeout=10m ./integration/...
```

In serial mode, go-adbtest attaches to an already running emulator or device and
does not stop it during teardown. Do not set the AVD-only variables in this mode:

```sh
ADBTEST_APK="$PWD/app/build/outputs/apk/debug/app-debug.apk" \
ADBTEST_SERIAL=emulator-5554 \
ADBTEST_PERMISSION_TRIGGER_SELECTOR='#request-permission' \
ADBTEST_RESULT_SELECTOR='#permission-status' \
ADBTEST_EXPECTED_TEXT=granted \
go test -tags=android_integration -count=1 -timeout=10m ./integration/...
```

The CI template uses serial mode because the emulator action owns the emulator.

To compile and exercise the example's configuration checks without Android SDK
tools, leave the fixture environment unset:

```sh
go test -tags=android_integration -count=1 ./examples
```

The device test will report a skip, while the AVD and serial configuration tests
still run. The workflow file remains inert while it is under `examples/`; GitHub
only discovers workflows placed in `.github/workflows/`.
