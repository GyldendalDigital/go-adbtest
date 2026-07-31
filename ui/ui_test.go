package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GyldendalDigital/go-adbtest/adb"
)

func TestDumpFastPathStripsTrailingStatus(t *testing.T) {
	fixture := writeFixture(t, "fast.xml", testDump)
	logPath := filepath.Join(t.TempDir(), "adb.log")
	t.Setenv("FAKE_ADB_XML", fixture)
	t.Setenv("FAKE_ADB_LOG", logPath)

	client := fakeADBClient(t, `
printf '%s|%s|%s\n' "$1" "$2" "${3-}" >> "$FAKE_ADB_LOG"
if [ "$1" = "shell" ] && [ "$2" = "uiautomator dump /dev/tty" ]; then
    cat "$FAKE_ADB_XML"
    printf '%s\n' 'UI hierchary dumped to: /dev/tty'
    exit 0
fi
exit 91
`)

	elements, err := NewInteractor(client).Dump()
	if err != nil {
		t.Fatalf("Dump() error: %v", err)
	}
	if len(elements) != 5 {
		t.Fatalf("Dump() returned %d elements, want 5", len(elements))
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := nonEmptyLines(string(logData))
	if len(lines) != 1 || !strings.Contains(lines[0], "uiautomator dump /dev/tty") {
		t.Fatalf("adb calls = %q, want only the fast dump", lines)
	}
}

func TestDumpFallsBackToPulledFileAfterFastPathParseError(t *testing.T) {
	fixture := writeFixture(t, "fallback.xml", testDump)
	logPath := filepath.Join(t.TempDir(), "adb.log")
	t.Setenv("FAKE_ADB_XML", fixture)
	t.Setenv("FAKE_ADB_LOG", logPath)

	client := fakeADBClient(t, `
printf '%s|%s|%s\n' "$1" "$2" "${3-}" >> "$FAKE_ADB_LOG"
if [ "$1" = "shell" ] && [ "$2" = "uiautomator dump /dev/tty" ]; then
    printf '%s\n' '<hierarchy><node text="bad" bounds="invalid" /></hierarchy>'
    printf '%s\n' 'UI hierchary dumped to: /dev/tty'
    exit 0
fi
if [ "$1" = "shell" ] && [ "$2" = "uiautomator dump /sdcard/ui.xml" ]; then
    exit 0
fi
if [ "$1" = "pull" ] && [ "$2" = "/sdcard/ui.xml" ]; then
    cp "$FAKE_ADB_XML" "$3"
    exit 0
fi
if [ "$1" = "shell" ] && [ "$2" = "rm -f /sdcard/ui.xml" ]; then
    exit 0
fi
exit 92
`)

	elements, err := NewInteractor(client).Dump()
	if err != nil {
		t.Fatalf("Dump() error: %v", err)
	}
	if len(elements) != 5 {
		t.Fatalf("Dump() returned %d elements, want 5", len(elements))
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := nonEmptyLines(string(logData))
	if len(lines) != 4 {
		t.Fatalf("adb calls = %q, want fast dump, fallback dump, pull, cleanup", lines)
	}
	if !strings.HasPrefix(lines[2], "pull|/sdcard/ui.xml|") {
		t.Fatalf("third adb call = %q, want pull", lines[2])
	}
	localPath := strings.TrimPrefix(lines[2], "pull|/sdcard/ui.xml|")
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Fatalf("temporary pulled file still exists at %q (stat error: %v)", localPath, err)
	}
}

func TestDumpRejectsMalformedBoundsFromPulledFile(t *testing.T) {
	fixture := writeFixture(t, "malformed.xml", `<hierarchy><node text="Danger" bounds="broken" /></hierarchy>`)
	logPath := filepath.Join(t.TempDir(), "adb.log")
	t.Setenv("FAKE_ADB_XML", fixture)
	t.Setenv("FAKE_ADB_LOG", logPath)

	client := fakeADBClient(t, `
printf '%s|%s|%s\n' "$1" "$2" "${3-}" >> "$FAKE_ADB_LOG"
if [ "$1" = "shell" ] && [ "$2" = "uiautomator dump /dev/tty" ]; then
    printf '%s\n' 'fast path unavailable'
    exit 0
fi
if [ "$1" = "shell" ] && [ "$2" = "uiautomator dump /sdcard/ui.xml" ]; then
    exit 0
fi
if [ "$1" = "pull" ] && [ "$2" = "/sdcard/ui.xml" ]; then
    cp "$FAKE_ADB_XML" "$3"
    exit 0
fi
if [ "$1" = "shell" ] && [ "$2" = "rm -f /sdcard/ui.xml" ]; then
    exit 0
fi
exit 93
`)

	elements, err := NewInteractor(client).Dump()
	if err == nil {
		t.Fatal("Dump() error = nil, want malformed bounds error")
	}
	if elements != nil {
		t.Fatalf("Dump() elements = %v, want nil", elements)
	}
	if !strings.Contains(err.Error(), "invalid bounds") {
		t.Fatalf("Dump() error = %v, want invalid bounds", err)
	}
}

func TestTypeTextShellQuotesLiteralArgument(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "adb.log")
	markerPath := filepath.Join(t.TempDir(), "must-not-exist")
	t.Setenv("FAKE_ADB_LOG", logPath)
	t.Setenv("FAKE_ADB_MARKER", markerPath)
	client := fakeADBClient(t, `
input() {
    if [ "$1" != "text" ]; then
        exit 94
    fi
    printf '%s' "$2" > "$FAKE_ADB_LOG"
}
eval "$2"
`)

	text := `hello world; $(touch "$FAKE_ADB_MARKER") 'quoted' & 100%`
	if err := NewInteractor(client).TypeText(text); err != nil {
		t.Fatalf("TypeText() error: %v", err)
	}

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != text {
		t.Fatalf("decoded text argument = %q, want %q", got, text)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("shell metacharacters were executed; marker stat error: %v", err)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: "", want: "''"},
		{value: "hello world", want: "'hello world'"},
		{value: "a'b", want: `'a'\''b'`},
		{value: "$(command); & |", want: "'$(command); & |'"},
	}
	for _, tc := range tests {
		if got := shellQuote(tc.value); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

func fakeADBClient(t *testing.T, body string) *adb.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adb")
	script := "#!/bin/sh\nset -eu\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &adb.Client{ADBPath: path}
}

func writeFixture(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func nonEmptyLines(value string) []string {
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
