package cdp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebViewSocketContextSearchesAllPIDsAndCustomInfix(t *testing.T) {
	adbClient := fakeCDPADB(t, `
if [ "${1-}" = "shell" ]; then
    case "${2-}" in
        "pidof com.example.app:webview") printf '%s\n' '202 101' ;;
        "cat /proc/net/unix") cat <<'EOF'
Num RefCount Protocol Flags Type St Inode Path
00000000: 00000002 00000000 00010000 0001 01 1 @webview_devtools_remote_1202
00000000: 00000002 00000000 00010000 0001 01 2 @webview_devtools_remote_custom_infix_101
00000000: 00000002 00000000 00010000 0001 01 3 @unrelated_socket_202
EOF
        ;;
        *) exit 91 ;;
    esac
    exit 0
fi
exit 92
`)

	socket, err := webViewSocketContext(context.Background(), adbClient, "com.example.app:webview")
	if err != nil {
		t.Fatalf("webViewSocketContext() error: %v", err)
	}
	if socket != "webview_devtools_remote_custom_infix_101" {
		t.Fatalf("webViewSocketContext() = %q, want custom socket for one of all returned PIDs", socket)
	}
}

func TestWebViewSocketContextRejectsAmbiguousMatches(t *testing.T) {
	adbClient := fakeCDPADB(t, `
if [ "${1-}" = "shell" ]; then
    case "${2-}" in
        "pidof com.example.app") printf '%s\n' '101 202' ;;
        "cat /proc/net/unix") cat <<'EOF'
00000000: 00000002 00000000 00010000 0001 01 1 @webview_devtools_remote_101
00000000: 00000002 00000000 00010000 0001 01 2 @webview_devtools_remote_renderer_202
EOF
        ;;
        *) exit 91 ;;
    esac
    exit 0
fi
exit 92
`)

	_, err := webViewSocketContext(context.Background(), adbClient, "com.example.app")
	var ambiguity *ambiguousWebViewSocketError
	if !errors.As(err, &ambiguity) {
		t.Fatalf("webViewSocketContext() error = %v, want ambiguity error", err)
	}
	for _, detail := range []string{
		"multiple WebView DevTools sockets",
		"webview_devtools_remote_101",
		"webview_devtools_remote_renderer_202",
	} {
		if !strings.Contains(err.Error(), detail) {
			t.Errorf("ambiguity error %q missing %q", err, detail)
		}
	}
}

func TestNewClientForProcessContextDoesNotRemoveExistingForward(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "adb.log")
	t.Setenv("FAKE_ADB_LOG", logPath)
	adbClient := fakeCDPADB(t, `
printf '%s\n' "$*" >> "$FAKE_ADB_LOG"
if [ "${1-}" = "shell" ]; then
    case "${2-}" in
        "pidof com.example.app:webview") printf '%s\n' '303' ;;
        "cat /proc/net/unix") printf '%s\n' '00000000: 00000002 00000000 00010000 0001 01 3 @webview_devtools_remote_package_suffix_303' ;;
        *) exit 91 ;;
    esac
    exit 0
fi
if [ "${1-}" = "forward" ] && [ "${2-}" = "--no-rebind" ]; then
    printf '%s\n' 'cannot rebind existing socket' >&2
    exit 1
fi
exit 92
`)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	_, err := NewClientForProcessContext(ctx, adbClient, "com.example.app", ":webview", 9222)
	if err == nil || !strings.Contains(err.Error(), "create exclusive CDP port forward") {
		t.Fatalf("NewClientForProcessContext() error = %v, want existing-port failure", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("existing-port failure took %v, want immediate error", elapsed)
	}

	logData, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	logText := string(logData)
	for _, call := range []string{
		"shell pidof com.example.app:webview",
		"forward --no-rebind tcp:9222 localabstract:webview_devtools_remote_package_suffix_303",
	} {
		if !strings.Contains(logText, call) {
			t.Errorf("ADB log missing %q:\n%s", call, logText)
		}
	}
	if strings.Contains(logText, "forward --remove") {
		t.Fatalf("constructor removed a forward it did not create:\n%s", logText)
	}
}

func TestClientCloseRemovesOnlyOwnedForwardOnce(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "adb.log")
	t.Setenv("FAKE_ADB_LOG", logPath)
	adbClient := fakeCDPADB(t, `
printf '%s\n' "$*" >> "$FAKE_ADB_LOG"
if [ "${1-}" = "forward" ]; then
    exit 0
fi
exit 93
`)

	unowned := &Client{ADB: adbClient, LocalPort: 9222}
	if err := unowned.Close(); err != nil {
		t.Fatalf("unowned Close() error: %v", err)
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unowned Close() invoked ADB, stat error = %v", err)
	}

	owned := &Client{ADB: adbClient, LocalPort: 9222, forwardOwned: true}
	if err := owned.Close(); err != nil {
		t.Fatalf("owned Close() error: %v", err)
	}
	if err := owned.Close(); err != nil {
		t.Fatalf("second owned Close() error: %v", err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(logData), "forward --remove tcp:9222"); count != 1 {
		t.Fatalf("owned forward removal count = %d, want 1:\n%s", count, logData)
	}
}

func TestReconnectFailureDoesNotRemoveUnownedForward(t *testing.T) {
	oldServer := mockCDPServer(t, echoHandler(map[string]any{}))
	defer oldServer.Close()
	oldConn, err := Connect(serverPort(t, oldServer))
	if err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(t.TempDir(), "adb.log")
	t.Setenv("FAKE_ADB_LOG", logPath)
	adbClient := fakeCDPADB(t, `
printf '%s\n' "$*" >> "$FAKE_ADB_LOG"
if [ "${1-}" = "shell" ]; then
    case "${2-}" in
        "pidof com.example.app") printf '%s\n' '404' ;;
        "cat /proc/net/unix") printf '%s\n' '00000000: 00000002 00000000 00010000 0001 01 4 @webview_devtools_remote_404' ;;
        *) exit 94 ;;
    esac
    exit 0
fi
if [ "${1-}" = "forward" ] && [ "${2-}" = "--no-rebind" ]; then
    printf '%s\n' 'cannot rebind existing socket' >&2
    exit 1
fi
exit 95
`)
	client := &Client{
		Conn:       oldConn,
		ADB:        adbClient,
		LocalPort:  9222,
		AppPackage: "com.example.app",
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = client.ReconnectContext(ctx)
	if err == nil || !strings.Contains(err.Error(), "create exclusive CDP port forward") {
		t.Fatalf("ReconnectContext() error = %v, want existing-port failure", err)
	}
	if client.Conn != nil {
		t.Fatal("failed reconnect retained its stale connection")
	}
	if client.forwardOwned {
		t.Fatal("failed reconnect claimed ownership of an existing forward")
	}
	logData, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(logData), "forward --remove") {
		t.Fatalf("failed reconnect removed an unowned forward:\n%s", logData)
	}
}
