package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"nhooyr.io/websocket"
)

// Conn manages a WebSocket connection to a WebView's DevTools endpoint.
type Conn struct {
	url     string
	ws      *websocket.Conn
	nextID  atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan json.RawMessage
	done    chan struct{}
}

// cdpMessage represents a CDP JSON-RPC response.
type cdpMessage struct {
	ID     int64            `json:"id"`
	Result json.RawMessage  `json:"result,omitempty"`
	Error  *cdpError        `json:"error,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Connect establishes a CDP connection. Discovers the WebSocket URL via
// http://localhost:<port>/json/list (first target).
func Connect(localPort int) (*Conn, error) {
	wsURL, err := discoverWSURL(localPort)
	if err != nil {
		return nil, err
	}
	return connectWS(wsURL)
}

// ConnectWithRetry retries connection until success or timeout.
func ConnectWithRetry(localPort int, timeout time.Duration) (*Conn, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := Connect(localPort)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("CDP connect timeout after %v: %w", timeout, lastErr)
}

// ConnectDirect connects to a known WebSocket URL without discovery.
func ConnectDirect(wsURL string) (*Conn, error) {
	return connectWS(wsURL)
}

// Send sends a CDP command and waits for its response.
func (c *Conn) Send(method string, params map[string]any) (json.RawMessage, error) {
	return c.SendWithTimeout(method, params, 30*time.Second)
}

// SendWithTimeout sends a CDP command with a custom timeout.
func (c *Conn) SendWithTimeout(method string, params map[string]any, timeout time.Duration) (json.RawMessage, error) {
	id := c.nextID.Add(1)

	ch := make(chan json.RawMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	msg := map[string]any{
		"id":     id,
		"method": method,
	}
	if params != nil {
		msg["params"] = params
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal CDP message: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := c.ws.Write(ctx, websocket.MessageText, data); err != nil {
		return nil, fmt.Errorf("write CDP message: %w", err)
	}

	select {
	case result := <-ch:
		return result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("CDP %s: timeout after %v", method, timeout)
	case <-c.done:
		return nil, fmt.Errorf("CDP connection closed")
	}
}

// Close closes the WebSocket connection.
func (c *Conn) Close() error {
	select {
	case <-c.done:
		// Already closed
		return nil
	default:
		close(c.done)
	}
	return c.ws.Close(websocket.StatusNormalClosure, "closing")
}

// readLoop reads messages from the WebSocket and dispatches responses.
func (c *Conn) readLoop() {
	for {
		select {
		case <-c.done:
			return
		default:
		}

		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			select {
			case <-c.done:
				return
			default:
				// Connection lost — close pending channels
				c.mu.Lock()
				for id, ch := range c.pending {
					close(ch)
					delete(c.pending, id)
				}
				c.mu.Unlock()
				return
			}
		}

		var msg cdpMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		// Only dispatch responses (messages with an ID)
		if msg.ID == 0 {
			continue // event, ignore for now
		}

		c.mu.Lock()
		ch, ok := c.pending[msg.ID]
		c.mu.Unlock()

		if ok {
			if msg.Error != nil {
				// Encode error as a JSON result so caller can handle it
				errJSON, _ := json.Marshal(msg.Error)
				ch <- errJSON
			} else {
				ch <- msg.Result
			}
		}
	}
}

func connectWS(wsURL string) (*Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("websocket dial %s: %w", wsURL, err)
	}
	// Remove read limit (CDP messages can be large)
	ws.SetReadLimit(-1)

	conn := &Conn{
		url:     wsURL,
		ws:      ws,
		pending: make(map[int64]chan json.RawMessage),
		done:    make(chan struct{}),
	}

	go conn.readLoop()
	return conn, nil
}

// target represents an entry from /json/list.
type target struct {
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	Type                 string `json:"type"`
}

func discoverWSURL(port int) (string, error) {
	url := fmt.Sprintf("http://localhost:%d/json/list", port)
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("discover CDP targets at %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read targets response: %w", err)
	}

	var targets []target
	if err := json.Unmarshal(body, &targets); err != nil {
		return "", fmt.Errorf("parse targets: %w", err)
	}

	if len(targets) == 0 {
		return "", fmt.Errorf("no CDP targets found at %s", url)
	}

	// Prefer "page" type targets
	for _, t := range targets {
		if t.Type == "page" && t.WebSocketDebuggerURL != "" {
			return t.WebSocketDebuggerURL, nil
		}
	}

	// Fall back to first target with a URL
	for _, t := range targets {
		if t.WebSocketDebuggerURL != "" {
			return t.WebSocketDebuggerURL, nil
		}
	}

	return "", fmt.Errorf("no targets with webSocketDebuggerUrl at %s", url)
}
