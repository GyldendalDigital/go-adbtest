package cdp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

// mockCDPServer creates a test HTTP server that:
// 1. Serves /json/list with a fake target pointing to /ws
// 2. Handles WebSocket at /ws, echoing CDP responses
func mockCDPServer(t *testing.T, handler func(ctx context.Context, ws *websocket.Conn)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	srv := httptest.NewServer(mux)

	// Serve /json/list with the WS URL pointing to this server
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		targets := []map[string]string{
			{"webSocketDebuggerUrl": wsURL, "type": "page"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(targets)
	})

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("websocket accept: %v", err)
			return
		}
		defer ws.CloseNow()
		handler(r.Context(), ws)
	})

	return srv
}

// echoHandler responds to each CDP request with a simple result.
func echoHandler(result map[string]any) func(ctx context.Context, ws *websocket.Conn) {
	return func(ctx context.Context, ws *websocket.Conn) {
		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}

			var msg struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(data, &msg); err != nil {
				return
			}

			resp := map[string]any{
				"id":     msg.ID,
				"result": result,
			}
			respData, _ := json.Marshal(resp)
			if err := ws.Write(ctx, websocket.MessageText, respData); err != nil {
				return
			}
		}
	}
}

func TestConn_SendReceive(t *testing.T) {
	srv := mockCDPServer(t, echoHandler(map[string]any{
		"result": map[string]any{
			"type":  "string",
			"value": "hello",
		},
	}))
	defer srv.Close()

	// Extract port from test server
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	portInt := 0
	for _, c := range port {
		portInt = portInt*10 + int(c-'0')
	}

	conn, err := Connect(portInt)
	if err != nil {
		t.Fatalf("Connect() error: %v", err)
	}
	defer conn.Close()

	resp, err := conn.Send("Runtime.evaluate", map[string]any{
		"expression": "1+1",
	})
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}

	if len(resp) == 0 {
		t.Fatal("Send() returned empty response")
	}
}

func TestConn_MultipleSends(t *testing.T) {
	srv := mockCDPServer(t, echoHandler(map[string]any{
		"result": map[string]any{"type": "number", "value": 42},
	}))
	defer srv.Close()

	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	portInt := 0
	for _, c := range port {
		portInt = portInt*10 + int(c-'0')
	}

	conn, err := Connect(portInt)
	if err != nil {
		t.Fatalf("Connect() error: %v", err)
	}
	defer conn.Close()

	// Send multiple commands — they should each get a unique response
	for i := 0; i < 5; i++ {
		resp, err := conn.Send("Runtime.evaluate", map[string]any{
			"expression": "test",
		})
		if err != nil {
			t.Fatalf("Send #%d error: %v", i, err)
		}
		if len(resp) == 0 {
			t.Fatalf("Send #%d returned empty", i)
		}
	}
}

func TestConn_Timeout(t *testing.T) {
	// Server that never responds
	srv := mockCDPServer(t, func(ctx context.Context, ws *websocket.Conn) {
		// Read messages but never reply
		for {
			_, _, err := ws.Read(ctx)
			if err != nil {
				return
			}
		}
	})
	defer srv.Close()

	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	portInt := 0
	for _, c := range port {
		portInt = portInt*10 + int(c-'0')
	}

	conn, err := Connect(portInt)
	if err != nil {
		t.Fatalf("Connect() error: %v", err)
	}
	defer conn.Close()

	_, err = conn.SendWithTimeout("Runtime.evaluate", nil, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error = %v, want timeout", err)
	}
}

func TestConnectDirect(t *testing.T) {
	srv := mockCDPServer(t, echoHandler(map[string]any{}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	conn, err := ConnectDirect(wsURL)
	if err != nil {
		t.Fatalf("ConnectDirect() error: %v", err)
	}
	defer conn.Close()

	resp, err := conn.Send("Test.method", nil)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if resp == nil {
		t.Fatal("Send() returned nil")
	}
}

func TestExtractValue_String(t *testing.T) {
	resp := json.RawMessage(`{"result":{"type":"string","value":"hello world"}}`)
	val, err := extractValue(resp)
	if err != nil {
		t.Fatalf("extractValue() error: %v", err)
	}
	if val != "hello world" {
		t.Errorf("extractValue() = %q, want %q", val, "hello world")
	}
}

func TestExtractValue_Number(t *testing.T) {
	resp := json.RawMessage(`{"result":{"type":"number","value":42}}`)
	val, err := extractValue(resp)
	if err != nil {
		t.Fatalf("extractValue() error: %v", err)
	}
	if val != "42" {
		t.Errorf("extractValue() = %q, want %q", val, "42")
	}
}

func TestExtractValue_Boolean(t *testing.T) {
	resp := json.RawMessage(`{"result":{"type":"boolean","value":true}}`)
	val, err := extractValue(resp)
	if err != nil {
		t.Fatalf("extractValue() error: %v", err)
	}
	if val != "true" {
		t.Errorf("extractValue() = %q, want %q", val, "true")
	}
}

func TestExtractValue_Exception(t *testing.T) {
	resp := json.RawMessage(`{"result":{"type":"undefined"},"exceptionDetails":{"text":"ReferenceError: x is not defined"}}`)
	_, err := extractValue(resp)
	if err == nil {
		t.Fatal("expected error for exception")
	}
	if !strings.Contains(err.Error(), "ReferenceError") {
		t.Errorf("error = %v, want ReferenceError", err)
	}
}

func TestDiscoverWSURL(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"webSocketDebuggerUrl":"ws://localhost:9222/devtools/page/ABC","type":"page"}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	portInt := 0
	for _, c := range port {
		portInt = portInt*10 + int(c-'0')
	}

	url, err := discoverWSURL(portInt)
	if err != nil {
		t.Fatalf("discoverWSURL() error: %v", err)
	}
	if url != "ws://localhost:9222/devtools/page/ABC" {
		t.Errorf("discoverWSURL() = %q", url)
	}
}

func TestDiscoverWSURL_NoTargets(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	portInt := 0
	for _, c := range port {
		portInt = portInt*10 + int(c-'0')
	}

	_, err := discoverWSURL(portInt)
	if err == nil {
		t.Fatal("expected error for no targets")
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		s, substr string
		want      bool
	}{
		{"hello world", "world", true},
		{"hello", "xyz", false},
		{"", "a", false},
		{"a", "", false},
		{"abc", "abc", true},
	}
	for _, tc := range tests {
		if got := contains(tc.s, tc.substr); got != tc.want {
			t.Errorf("contains(%q, %q) = %v, want %v", tc.s, tc.substr, got, tc.want)
		}
	}
}
