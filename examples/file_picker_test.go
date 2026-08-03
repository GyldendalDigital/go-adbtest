//go:build android_integration

package examples_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	adbtest "github.com/GyldendalDigital/go-adbtest"
)

const filePickerFixtureScript = `(() => {
  document.body.innerHTML = '<main style="padding:48px">' +
    '<h1>go-adbtest file picker</h1>' +
    '<input id="adbtest-file-input" type="file" multiple style="font-size:24px;min-height:72px">' +
    '<p id="adbtest-file-result">No file selected</p></main>';
  document.getElementById('adbtest-file-input').addEventListener('change', function () {
    const files = Array.from(this.files || []);
    document.getElementById('adbtest-file-result').textContent = files.length
      ? 'Selected ' + files.length + ': ' + files.map(file => file.name).join(', ')
      : 'No file selected';
  });
  return 'ready';
})()`

// TestWebViewMultiFilePickerRoundTrip crosses both automation layers without
// requiring fixture-specific application HTML: CDP injects a temporary file
// input, a real mouse gesture opens Android DocumentsUI, native UI gestures
// select three pushed files, and CDP verifies the result back in the WebView.
func TestWebViewMultiFilePickerRoundTrip(t *testing.T) {
	device := requireExampleDevice(t)
	if !exampleFixture.filePickerEnabled {
		t.Skip("set ADBTEST_FILE_PICKER=true to run the optional DocumentsUI round trip")
	}
	device.RestartApp(t)

	interactionTimeout := exampleFixture.interactionTimeout
	device.CDP.WaitForSelector(t, "html", interactionTimeout)
	if got := device.CDP.Eval(t, filePickerFixtureScript); got != "ready" {
		t.Fatalf("inject file-picker fixture = %q, want ready", got)
	}

	prefix := fmt.Sprintf("adbtest-%d-%d", os.Getpid(), time.Now().UnixNano())
	names := []string{prefix + "-alpha.txt", prefix + "-beta.txt", prefix + "-gamma.txt"}
	pushFilePickerFixtures(t, device, names)

	device.CDP.WaitForSelector(t, "#adbtest-file-input", interactionTimeout)
	device.CDP.Click(t, "#adbtest-file-input")
	device.UI.WaitForText(t, names[0], interactionTimeout)
	device.UI.LongPressOnText(t, names[0], interactionTimeout)
	device.UI.TapOnText(t, names[1], interactionTimeout)
	device.UI.TapOnText(t, names[2], interactionTimeout)
	tapOnResourceIDSuffix(t, device, ":id/action_menu_select", interactionTimeout)

	result := device.CDP.WaitForText(
		t,
		"#adbtest-file-result",
		"Selected 3:",
		interactionTimeout,
	)
	for _, want := range append([]string{"Selected 3:"}, names...) {
		if !strings.Contains(result, want) {
			t.Fatalf("file-picker result %q does not contain %q", result, want)
		}
	}
}

func tapOnResourceIDSuffix(
	t *testing.T,
	device *adbtest.Device,
	suffix string,
	timeout time.Duration,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		elements, err := device.UI.DumpContext(ctx)
		if err == nil {
			for _, element := range elements {
				if strings.HasSuffix(element.ResourceID, suffix) {
					if err := device.UI.TapContext(
						ctx,
						element.Bounds.CenterX(),
						element.Bounds.CenterY(),
					); err != nil {
						t.Fatalf("tap DocumentsUI resource ID suffix %q: %v", suffix, err)
					}
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for DocumentsUI resource ID suffix %q: %v", suffix, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func pushFilePickerFixtures(t *testing.T, device *adbtest.Device, names []string) {
	t.Helper()
	remotePaths := make([]string, 0, len(names))
	t.Cleanup(func() {
		if len(remotePaths) == 0 {
			return
		}
		if _, err := device.ADB.Shell("rm -f " + strings.Join(remotePaths, " ")); err != nil {
			t.Errorf("remove device file-picker fixtures: %v", err)
		}
	})
	for _, name := range names {
		localPath := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(localPath, []byte("go-adbtest fixture "+name+"\n"), 0o600); err != nil {
			t.Fatalf("write host file-picker fixture %q: %v", name, err)
		}
		remotePath := "/sdcard/Download/" + name
		remotePaths = append(remotePaths, remotePath)
		if err := device.ADB.Push(localPath, remotePath); err != nil {
			t.Fatalf("push file-picker fixture %q: %v", name, err)
		}
	}
}
