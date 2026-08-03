package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/adb"
	"github.com/coder/websocket"
)

func TestConnProtocolErrorIsReturned(t *testing.T) {
	srv := mockCDPServer(t, func(ctx context.Context, ws *websocket.Conn) {
		request := readCDPRequest(t, ctx, ws)
		writeCDPMessage(t, ctx, ws, map[string]any{
			"id": request.ID,
			"error": map[string]any{
				"code":    -32601,
				"message": "method not found",
			},
		})
	})
	defer srv.Close()

	conn, err := Connect(serverPort(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	result, err := conn.Send("Missing.method", nil)
	if err == nil {
		t.Fatalf("Send() = (%s, nil), want protocol error", result)
	}
	if result != nil {
		t.Fatalf("Send() result = %s, want nil", result)
	}
	var protocolErr *cdpError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("Send() error = %v, want *cdpError", err)
	}
	if protocolErr.Code != -32601 || protocolErr.Message != "method not found" {
		t.Fatalf("protocol error = %+v", protocolErr)
	}
}

func TestConnDisconnectFailsAllPendingSends(t *testing.T) {
	const sendCount = 12
	srv := mockCDPServer(t, func(ctx context.Context, ws *websocket.Conn) {
		for range sendCount {
			readCDPRequest(t, ctx, ws)
		}
		if err := ws.CloseNow(); err != nil {
			t.Errorf("CloseNow() error: %v", err)
		}
	})
	defer srv.Close()

	conn, err := Connect(serverPort(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	errs := make(chan error, sendCount)
	var wg sync.WaitGroup
	for i := range sendCount {
		wg.Add(1)
		go func(token int) {
			defer wg.Done()
			result, err := conn.SendWithTimeout("Pending.method", map[string]any{"token": token}, 2*time.Second)
			if err == nil {
				errs <- fmt.Errorf("send %d returned false success: %s", token, result)
				return
			}
			if result != nil {
				errs <- fmt.Errorf("send %d returned result %s with error %v", token, result, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestConnMalformedMessageTerminatesUnderlyingWebSocket(t *testing.T) {
	serverSawClose := make(chan error, 1)
	srv := mockCDPServer(t, func(ctx context.Context, ws *websocket.Conn) {
		readCDPRequest(t, ctx, ws)
		if err := ws.Write(ctx, websocket.MessageText, []byte(`{"not-json"`)); err != nil {
			t.Errorf("write malformed message: %v", err)
			return
		}
		closeCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		_, _, err := ws.Read(closeCtx)
		serverSawClose <- err
	})
	defer srv.Close()

	conn, err := Connect(serverPort(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	result, err := conn.SendWithTimeout("Malformed.response", nil, time.Second)
	if err == nil || !strings.Contains(err.Error(), "decode CDP message") {
		t.Fatalf("Send() = (%s, %v), want decode error", result, err)
	}
	select {
	case err := <-serverSawClose:
		if err == nil {
			t.Fatal("server read returned nil, want client websocket closure")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client did not close underlying websocket after malformed message")
	}
}

func TestConnRoutesConcurrentResponsesByID(t *testing.T) {
	const sendCount = 24
	type request struct {
		ID     int64
		Params struct {
			Token int `json:"token"`
		}
	}

	srv := mockCDPServer(t, func(ctx context.Context, ws *websocket.Conn) {
		requests := make([]request, 0, sendCount)
		for range sendCount {
			var item request
			readCDPRequestInto(t, ctx, ws, &item)
			requests = append(requests, item)
		}
		for i := len(requests) - 1; i >= 0; i-- {
			writeCDPMessage(t, ctx, ws, map[string]any{
				"id":     requests[i].ID,
				"result": map[string]any{"token": requests[i].Params.Token},
			})
		}
	})
	defer srv.Close()

	conn, err := Connect(serverPort(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	errs := make(chan error, sendCount)
	var wg sync.WaitGroup
	for i := range sendCount {
		wg.Add(1)
		go func(token int) {
			defer wg.Done()
			result, err := conn.SendWithTimeout("Concurrent.method", map[string]any{"token": token}, 2*time.Second)
			if err != nil {
				errs <- fmt.Errorf("send %d: %w", token, err)
				return
			}
			var response struct {
				Token int `json:"token"`
			}
			if err := json.Unmarshal(result, &response); err != nil {
				errs <- fmt.Errorf("send %d decode %s: %w", token, result, err)
				return
			}
			if response.Token != token {
				errs <- fmt.Errorf("send %d received token %d", token, response.Token)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestConnConcurrentCloseIsSafeAndIdempotent(t *testing.T) {
	srv := mockCDPServer(t, func(ctx context.Context, ws *websocket.Conn) {
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				return
			}
		}
	})
	defer srv.Close()

	conn, err := Connect(serverPort(t, srv))
	if err != nil {
		t.Fatal(err)
	}

	const closeCount = 16
	errs := make(chan error, closeCount)
	var wg sync.WaitGroup
	for range closeCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- conn.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Close() error: %v", err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("repeated Close() error: %v", err)
	}
}

func TestDiscoverWSURLRejectsHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, err := discoverWSURL(context.Background(), serverPort(t, srv))
	if err == nil || !strings.Contains(err.Error(), "503 Service Unavailable") {
		t.Fatalf("discoverWSURL() error = %v, want HTTP 503", err)
	}
}

func TestDiscoverWSURLHonorsContextTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := discoverWSURL(ctx, serverPort(t, srv))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("discoverWSURL() error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("discoverWSURL() took %v, context was 50ms", elapsed)
	}
}

func TestConnectWithRetryHonorsOneTotalTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	started := time.Now()
	_, err := ConnectWithRetry(serverPort(t, srv), 75*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ConnectWithRetry() error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("ConnectWithRetry() took %v, total timeout was 75ms", elapsed)
	}
}

func TestContextAPIsRejectInvalidInputs(t *testing.T) {
	//nolint:staticcheck // A defensive API test intentionally supplies nil.
	if _, err := ConnectContext(nil, 9222); err == nil || !strings.Contains(err.Error(), "nil context") {
		t.Fatalf("ConnectContext(nil) error = %v", err)
	}
	if _, err := ConnectContext(context.Background(), 0); err == nil || !strings.Contains(err.Error(), "invalid local port") {
		t.Fatalf("ConnectContext(port 0) error = %v", err)
	}
	if _, err := ConnectWithRetryContext(context.Background(), 65536); err == nil || !strings.Contains(err.Error(), "invalid local port") {
		t.Fatalf("ConnectWithRetryContext(port 65536) error = %v", err)
	}
	if _, err := NewClientContext(context.Background(), nil, "com.example.app", 9222); err == nil || !strings.Contains(err.Error(), "nil ADB client") {
		t.Fatalf("NewClientContext(nil ADB) error = %v", err)
	}
	unusedADB := &adb.Client{ADBPath: "/must/not/run"}
	if _, err := NewClientContext(context.Background(), unusedADB, "com.example;reboot", 9222); err == nil || !strings.Contains(err.Error(), "invalid app package") {
		t.Fatalf("NewClientContext(invalid package) error = %v", err)
	}
}

func TestSendContextHonorsTimeout(t *testing.T) {
	srv := mockCDPServer(t, func(ctx context.Context, ws *websocket.Conn) {
		readCDPRequest(t, ctx, ws)
		_, _, _ = ws.Read(ctx)
	})
	defer srv.Close()

	conn, err := Connect(serverPort(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = conn.SendContext(ctx, "Never.responds", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendContext() error = %v, want context deadline", err)
	}
}

func TestSendContextTimeoutDoesNotPoisonConnection(t *testing.T) {
	srv := mockCDPServer(t, func(ctx context.Context, ws *websocket.Conn) {
		readCDPRequest(t, ctx, ws)
		second := readCDPRequest(t, ctx, ws)
		writeCDPMessage(t, ctx, ws, map[string]any{
			"id":     second.ID,
			"result": map[string]any{"ok": true},
		})
	})
	defer srv.Close()

	conn, err := Connect(serverPort(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := conn.SendContext(ctx, "Never.responds", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first SendContext() error = %v, want context deadline", err)
	}
	if _, err := conn.SendWithTimeout("Still.works", nil, time.Second); err != nil {
		t.Fatalf("second Send() after caller timeout: %v", err)
	}
}

func TestHandleWriteFailureClassifiesCallerCancellation(t *testing.T) {
	cancelled := &Conn{pending: make(map[int64]chan commandResponse), done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := cancelled.handleWriteFailure(ctx, errors.New("write failed")); !errors.Is(err, context.Canceled) {
		t.Fatalf("handleWriteFailure(cancelled) error = %v, want context cancellation", err)
	}
	select {
	case <-cancelled.done:
		t.Fatal("caller cancellation terminated the connection")
	default:
	}
	if cancelled.terminalErr != nil {
		t.Fatalf("caller cancellation terminal error = %v", cancelled.terminalErr)
	}

	failed := &Conn{pending: make(map[int64]chan commandResponse), done: make(chan struct{})}
	writeErr := errors.New("broken transport")
	if err := failed.handleWriteFailure(context.Background(), writeErr); !errors.Is(err, writeErr) {
		t.Fatalf("handleWriteFailure(transport) error = %v, want write error", err)
	}
	select {
	case <-failed.done:
	default:
		t.Fatal("genuine write failure did not terminate the connection")
	}
	if !errors.Is(failed.terminalErr, writeErr) {
		t.Fatalf("genuine write failure terminal error = %v", failed.terminalErr)
	}
}

func TestEvalContextRejectsMissingConnection(t *testing.T) {
	for _, client := range []*Client{nil, {}} {
		if value, err := client.EvalContext(context.Background(), "1+1"); err == nil {
			t.Fatalf("EvalContext() = %q, nil; want missing connection error", value)
		}
	}
	if _, err := (&Conn{}).SendContext(context.Background(), "Runtime.evaluate", nil); err == nil {
		t.Fatal("zero Conn.SendContext() error = nil, want uninitialized connection error")
	}
}

func TestWaitHelpersHonorOneTotalDeadline(t *testing.T) {
	for _, test := range []struct {
		name string
		wait func(context.Context, *Client) error
	}{
		{
			name: "selector",
			wait: func(ctx context.Context, client *Client) error {
				return client.waitForSelectorContext(ctx, "#never")
			},
		},
		{
			name: "text",
			wait: func(ctx context.Context, client *Client) error {
				_, err := client.waitForTextContext(ctx, "#never", "missing")
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := mockCDPServer(t, func(ctx context.Context, ws *websocket.Conn) {
				readCDPRequest(t, ctx, ws)
				_, _, _ = ws.Read(ctx)
			})
			defer srv.Close()

			conn, err := Connect(serverPort(t, srv))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
			defer cancel()
			started := time.Now()
			err = test.wait(ctx, &Client{Conn: conn})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("wait error = %v, want context deadline", err)
			}
			if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
				t.Fatalf("wait took %v with a 75ms deadline", elapsed)
			}
		})
	}
}

func TestWaitForSelectorUsesVisibilityExpression(t *testing.T) {
	expressions := make(chan string, 1)
	srv := mockCDPServer(t, func(ctx context.Context, ws *websocket.Conn) {
		var request struct {
			ID     int64 `json:"id"`
			Params struct {
				Expression string `json:"expression"`
			} `json:"params"`
		}
		readCDPRequestInto(t, ctx, ws, &request)
		expressions <- request.Params.Expression
		writeCDPMessage(t, ctx, ws, map[string]any{
			"id": request.ID,
			"result": map[string]any{
				"result": map[string]any{"type": "boolean", "value": true},
			},
		})
	})
	defer srv.Close()

	conn, err := Connect(serverPort(t, srv))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	client := &Client{Conn: conn}
	client.WaitForSelector(t, `#dialog[data-label="ready"]`, time.Second)
	expression := <-expressions
	for _, required := range []string{
		`document.querySelector("#dialog[data-label=\"ready\"]")`,
		"getComputedStyle",
		"current = current.parentElement",
		"getBoundingClientRect",
		`style.display === "none"`,
		`style.visibility === "hidden"`,
		"style.opacity",
		"rect.width > 0",
		"rect.height > 0",
	} {
		if !strings.Contains(expression, required) {
			t.Errorf("visibility expression missing %q:\n%s", required, expression)
		}
	}
}

func TestNewClientContextPollsPIDAndReconnects(t *testing.T) {
	srv := mockCDPServer(t, echoHandler(map[string]any{}))
	defer srv.Close()

	statePath := filepath.Join(t.TempDir(), "pid-attempts")
	t.Setenv("FAKE_ADB_STATE", statePath)
	adbClient := fakeCDPADB(t, `
if [ "${1-}" = "shell" ]; then
    if [ "${2-}" = "cat /proc/net/unix" ]; then
        printf '%s\n' '00000000: 00000002 00000000 00010000 0001 01 4242 @webview_devtools_remote_4242'
        exit 0
    fi
    count=0
    if [ -f "$FAKE_ADB_STATE" ]; then
        count=$(sed -n '1p' "$FAKE_ADB_STATE")
    fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$FAKE_ADB_STATE"
    if [ "$count" -lt 2 ]; then
        exit 1
    fi
    printf '%s\n' '4242'
    exit 0
fi
if [ "${1-}" = "forward" ]; then
    exit 0
fi
exit 95
`)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, err := NewClientContext(ctx, adbClient, "com.example.app", serverPort(t, srv))
	if err != nil {
		t.Fatalf("NewClientContext() error: %v", err)
	}
	defer client.Close()

	reconnectCtx, reconnectCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer reconnectCancel()
	if err := client.ReconnectContext(reconnectCtx); err != nil {
		t.Fatalf("ReconnectContext() error: %v", err)
	}
}

func TestNewClientContextHonorsTimeoutWhilePollingPID(t *testing.T) {
	adbClient := fakeCDPADB(t, `
if [ "${1-}" = "shell" ]; then
    exit 1
fi
if [ "${1-}" = "forward" ]; then
    exit 0
fi
exit 96
`)

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := NewClientContext(ctx, adbClient, "com.example.app", 9222)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("NewClientContext() error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("NewClientContext() took %v, context was 75ms", elapsed)
	}
}

func TestNewClientContextCleansForwardAfterReadinessTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	logPath := filepath.Join(t.TempDir(), "adb.log")
	t.Setenv("FAKE_ADB_LOG", logPath)
	adbClient := fakeCDPADB(t, `
printf '%s\n' "$*" >> "$FAKE_ADB_LOG"
if [ "${1-}" = "shell" ]; then
    case "${2-}" in
        "pidof com.example.app") printf '%s\n' '4242' ;;
        "cat /proc/net/unix") printf '%s\n' '00000000: 00000002 00000000 00010000 0001 01 4242 @webview_devtools_remote_4242' ;;
        *) exit 97 ;;
    esac
    exit 0
fi
if [ "${1-}" = "forward" ]; then
    exit 0
fi
exit 97
`)

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	_, err := NewClientContext(ctx, adbClient, "com.example.app", serverPort(t, srv))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("NewClientContext() error = %v, want context deadline", err)
	}
	logData, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	logText := string(logData)
	if !strings.Contains(logText, "forward --no-rebind tcp:") {
		t.Fatalf("ADB calls did not create an exclusive forward:\n%s", logData)
	}
	if count := strings.Count(logText, "forward --remove tcp:"); count != 1 {
		t.Fatalf("ADB calls removed the owned forward %d times, want 1:\n%s", count, logData)
	}
}

func TestReconnectContextFailureClearsConnectionAndCleansForward(t *testing.T) {
	oldServer := mockCDPServer(t, echoHandler(map[string]any{}))
	defer oldServer.Close()
	oldConn, err := Connect(serverPort(t, oldServer))
	if err != nil {
		t.Fatal(err)
	}

	notReadyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer notReadyServer.Close()

	logPath := filepath.Join(t.TempDir(), "adb.log")
	t.Setenv("FAKE_ADB_LOG", logPath)
	adbClient := fakeCDPADB(t, `
printf '%s\n' "$*" >> "$FAKE_ADB_LOG"
if [ "${1-}" = "shell" ]; then
    case "${2-}" in
        "pidof com.example.app") printf '%s\n' '4242' ;;
        "cat /proc/net/unix") printf '%s\n' '00000000: 00000002 00000000 00010000 0001 01 4242 @webview_devtools_remote_4242' ;;
        *) exit 98 ;;
    esac
    exit 0
fi
if [ "${1-}" = "forward" ]; then
    exit 0
fi
exit 98
`)
	client := &Client{
		Conn:          oldConn,
		ADB:           adbClient,
		LocalPort:     serverPort(t, notReadyServer),
		AppPackage:    "com.example.app",
		forwardOwned:  true,
		forwardSocket: "webview_devtools_remote_1111",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	err = client.ReconnectContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ReconnectContext() error = %v, want context deadline", err)
	}
	if client.Conn != nil {
		t.Fatal("ReconnectContext() left a stale connection after failure")
	}
	logData, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if count := strings.Count(string(logData), "forward --remove tcp:"); count < 2 {
		t.Fatalf("ADB calls contain %d forward removals, want old and failed-new cleanup:\n%s", count, logData)
	}
}

func TestClientCloseContextIsNilSafeAndBounded(t *testing.T) {
	var nilClient *Client
	if err := nilClient.Close(); err != nil {
		t.Fatalf("nil Client.Close() error: %v", err)
	}

	adbClient := fakeCDPADB(t, `
if [ "${1-}" = "forward" ]; then
    while :; do :; done
fi
exit 99
`)
	client := &Client{ADB: adbClient, LocalPort: 9222, forwardOwned: true}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := client.CloseContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CloseContext() error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("CloseContext() took %v, context was 50ms", elapsed)
	}
}

func TestClientCloseReturnsForwardRemovalError(t *testing.T) {
	adbClient := fakeCDPADB(t, `
if [ "${1-}" = "forward" ]; then
    printf '%s\n' 'remove failed' >&2
    exit 7
fi
exit 100
`)
	client := &Client{ADB: adbClient, LocalPort: 9222, forwardOwned: true}
	err := client.Close()
	if err == nil || !strings.Contains(err.Error(), "remove CDP port forward") || !strings.Contains(err.Error(), "remove failed") {
		t.Fatalf("Close() error = %v, want forward removal error", err)
	}
}

func serverPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, portString, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

type cdpRequest struct {
	ID int64 `json:"id"`
}

func readCDPRequest(t *testing.T, ctx context.Context, ws *websocket.Conn) cdpRequest {
	t.Helper()
	var request cdpRequest
	readCDPRequestInto(t, ctx, ws, &request)
	return request
}

func readCDPRequestInto(t *testing.T, ctx context.Context, ws *websocket.Conn, destination any) {
	t.Helper()
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Errorf("read CDP request: %v", err)
		return
	}
	if err := json.Unmarshal(data, destination); err != nil {
		t.Errorf("decode CDP request: %v", err)
	}
}

func writeCDPMessage(t *testing.T, ctx context.Context, ws *websocket.Conn, message any) {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Errorf("encode CDP response: %v", err)
		return
	}
	if err := ws.Write(ctx, websocket.MessageText, data); err != nil {
		t.Errorf("write CDP response: %v", err)
	}
}

func fakeCDPADB(t *testing.T, body string) *adb.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adb")
	script := "#!/bin/sh\nset -eu\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &adb.Client{ADBPath: path}
}
