# Android WebView integration example

This directory contains a generic consumer template for testing an Android app
whose UI is rendered in a debuggable WebView. The tests demonstrate app
restart and CDP interaction, plus opt-in WebView-to-DocumentsUI multi-file and
Android runtime-permission flows.

The example is guarded by the `android_integration` build tag. It is therefore
excluded from ordinary `go test ./...` runs. Even with the tag enabled, it safely
skips the device tests when their required APK/device values are absent.

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
cp examples/file_picker_test.go integration/file_picker_test.go
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

## Required environment

| Variable | Meaning |
| --- | --- |
| `ADBTEST_APK` | Path to the APK under test. An absolute path is recommended. |
| `ADBTEST_AVD` or `ADBTEST_SERIAL` | Exactly one device mode; see below. |

Those two values are sufficient for the always-on lifecycle/CDP smoke.

Optional settings are:

| Variable | Default | Meaning |
| --- | --- | --- |
| `ADBTEST_APP_PACKAGE` | Read from the APK | Android application package. Setting it explicitly is recommended in CI. |
| `ADBTEST_APP_PROCESS` | Application package | Process hosting the WebView. Use a relative name such as `:webview` for a secondary process. |
| `ADBTEST_APP_ACTIVITY` | APK metadata or device resolution | Launch activity. Setting it explicitly is recommended in CI and on older Android releases. |
| `ADBTEST_INTERACTION_TIMEOUT` | `30s` | Timeout for selector, native picker, text, and permission interaction. |
| `ADBTEST_BOOT_TIMEOUT` | Library default | Emulator/device boot timeout. |
| `ADBTEST_APP_TIMEOUT` | Library default | Application/CDP startup timeout. |
| `ADBTEST_CDP_PORT` | Library default | Local CDP forwarding port. |
| `ADBTEST_FILE_PICKER` | `false` | Run the optional DocumentsUI multi-file round trip. |
| `ADBTEST_GPU` | `auto` | AVD GPU mode; valid only with `ADBTEST_AVD`. |
| `ADBTEST_CORES` | `2` | Owned-AVD virtual CPU cap; `0` uses the AVD setting. |
| `ADBTEST_MEMORY_MB` | AVD/system image | Advanced RAM override (`0` or 1536–8192); Android may enforce a higher minimum. |
| `ADBTEST_ACCELERATION` | `on` | VM acceleration mode (`auto`, `on`, or `off`); `on` fails fast without a usable hypervisor. |
| `ADBTEST_HEADLESS` | `true` | Start an owned AVD without a window. |
| `ADBTEST_NO_AUDIO` | `true` | Disable audio for an owned AVD. |
| `ADBTEST_WIPE_DATA` | `false` | Wipe an owned AVD before starting it. |
| `ADBTEST_NO_SNAPSHOT` | `true` | Cold-boot an owned AVD without loading or saving quick-boot state. |

The runtime-permission test is optional. Set all three variables together to
enable it:

| Variable | Meaning |
| --- | --- |
| `ADBTEST_PERMISSION_TRIGGER_SELECTOR` | CSS selector for a control that requests an Android runtime permission. |
| `ADBTEST_RESULT_SELECTOR` | CSS selector whose text reports the result. |
| `ADBTEST_EXPECTED_TEXT` | Text expected within the result element. |

Set `ADBTEST_FILE_PICKER=true` to enable the separate multi-file example. It
injects a temporary `<input type="file" multiple>` fixture, pushes three
uniquely named text files, opens DocumentsUI with a real CDP gesture, selects
them through native UI automation, and verifies the result in the WebView. The
app must install a `WebChromeClient` that handles WebView file chooser requests,
and the test device must provide DocumentsUI. No dedicated fixture APK or
bundled Android project is needed beyond the consumer app under test.

The permission trigger must actually cause the application to request a runtime
permission. `GrantAll` safely returns when no permission dialog appears, but in
that case the example is no longer exercising the intended permission flow.
For Android's limited photo/video choice, call `GrantSelected` and complete the
system picker instead of using this example's full-access `GrantAll` flow.
The multi-file example exercises DocumentsUI; it is separate from
`permissions.Handler` and is not a runtime-permission test.

## Device modes

In AVD mode, go-adbtest starts, owns, and stops the emulator:

Create the lightweight local AVD once (the system-image download is
intentionally explicit and outside `Setup`):

```sh
sdkmanager 'system-images;android-34;google_apis;x86_64'
echo no | avdmanager create avd \
  --name small_phone_api_34 \
  --package 'system-images;android-34;google_apis;x86_64' \
  --device small_phone
emulator -accel-check
```

Then run the suite:

```sh
ADBTEST_APK="$PWD/app/build/outputs/apk/debug/app-debug.apk" \
ADBTEST_AVD=small_phone_api_34 \
ADBTEST_APP_TIMEOUT=2m \
ADBTEST_FILE_PICKER=true \
go test -p=1 -tags=android_integration -count=1 -timeout=10m ./integration/...
```

`HeadlessAVD` supplies the safe AVD defaults used by the template. The named
AVD must already exist: Setup starts, waits for, owns, and stops it, but does
not download a system image or create an AVD. Prefer a 720×1280 `small_phone`
profile with a non-Play-Store `google_apis` x86_64 image. On a runner that
cannot use automatic graphics selection, set `ADBTEST_GPU=swiftshader`;
`swiftshader_indirect` is deprecated. The safe profile also uses `-accel on`,
so it refuses to start rather than silently using CPU emulation when the host
hypervisor is unavailable.

In serial mode, go-adbtest attaches to an already running emulator or device and
does not stop it during teardown. Do not set the AVD-only variables in this mode:

```sh
ADBTEST_APK="$PWD/app/build/outputs/apk/debug/app-debug.apk" \
ADBTEST_SERIAL=emulator-5554 \
go test -p=1 -tags=android_integration -count=1 -timeout=10m ./integration/...
```

The CI template uses serial mode because the emulator action creates and owns
the emulator. Its `profile: small_phone` input is a hardware-profile ID, not an
AVD name for `HeadlessAVD`. The template fixes `emulator-port: 5554` and uses
the action's `EMULATOR_PORT` value to select `emulator-5554` deterministically.
Setup installs with `adb install -r`, which retains existing application data
and permission state. Reset that state explicitly when test isolation requires
it. The configured CDP port must also be free; go-adbtest will not replace a
pre-existing ADB forward.

To compile and exercise the example's configuration checks without Android SDK
tools, leave the fixture environment unset:

```sh
go test -p=1 -tags=android_integration -count=1 ./examples
```

The device test will report a skip, while the AVD and serial configuration tests
still run. The workflow file remains inert while it is under `examples/`; GitHub
only discovers workflows placed in `.github/workflows/`.
