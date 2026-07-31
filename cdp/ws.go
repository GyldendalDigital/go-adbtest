package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"nhooyr.io/websocket"
)

const (
	defaultConnectTimeout = 10 * time.Second
	connectRetryInterval  = 250 * time.Millisecond
	maxTargetListSize     = 4 << 20
)

var errConnectionClosed = errors.New("CDP connection closed")

type commandResponse struct {
	result json.RawMessage
	err    error
}

// Conn manages a WebSocket connection to a WebView's DevTools endpoint.
type Conn struct {
	ws     *websocket.Conn
	nextID atomic.Int64

	mu          sync.Mutex
	pending     map[int64]chan commandResponse
	terminalErr error
	done        chan struct{}

	terminateOnce sync.Once
	wsCloseOnce   sync.Once
	wsCloseErr    error
}

// cdpMessage represents a CDP JSON-RPC response.
type cdpMessage struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *cdpError       `json:"error,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *cdpError) Error() string {
	return fmt.Sprintf("CDP protocol error %d: %s", e.Code, e.Message)
}

// Connect establishes a CDP connection. Discovers the WebSocket URL via
// http://localhost:<port>/json/list (first target).
func Connect(localPort int) (*Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultConnectTimeout)
	defer cancel()
	return ConnectContext(ctx, localPort)
}

// ConnectContext establishes a CDP connection within ctx.
func ConnectContext(ctx context.Context, localPort int) (*Conn, error) {
	if ctx == nil {
		return nil, errors.New("connect CDP: nil context")
	}
	if err := validateLocalPort(localPort); err != nil {
		return nil, err
	}
	wsURL, err := discoverWSURL(ctx, localPort)
	if err != nil {
		return nil, err
	}
	return connectWS(ctx, wsURL)
}

// ConnectWithRetry retries connection until success or timeout.
func ConnectWithRetry(localPort int, timeout time.Duration) (*Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	conn, err := ConnectWithRetryContext(ctx, localPort)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("CDP connect timeout after %v: %w", timeout, err)
	}
	return conn, err
}

// ConnectWithRetryContext retries connection while honoring one total context
// deadline across target discovery, dialing, and retry delays.
func ConnectWithRetryContext(ctx context.Context, localPort int) (*Conn, error) {
	if ctx == nil {
		return nil, errors.New("connect to CDP: nil context")
	}
	if err := validateLocalPort(localPort); err != nil {
		return nil, err
	}
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return nil, retryError("connect to CDP", err, lastErr)
		}

		conn, err := ConnectContext(ctx, localPort)
		if err == nil {
			return conn, nil
		}
		lastErr = err

		if err := waitForRetry(ctx); err != nil {
			return nil, retryError("connect to CDP", err, lastErr)
		}
	}
}

// ConnectDirect connects to a known WebSocket URL without discovery.
func ConnectDirect(wsURL string) (*Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultConnectTimeout)
	defer cancel()
	return ConnectDirectContext(ctx, wsURL)
}

// ConnectDirectContext connects to a known WebSocket URL within ctx.
func ConnectDirectContext(ctx context.Context, wsURL string) (*Conn, error) {
	if ctx == nil {
		return nil, errors.New("connect directly to CDP: nil context")
	}
	return connectWS(ctx, wsURL)
}

// Send sends a CDP command and waits for its response.
func (c *Conn) Send(method string, params map[string]any) (json.RawMessage, error) {
	return c.SendWithTimeout(method, params, 30*time.Second)
}

// SendWithTimeout sends a CDP command with a custom timeout.
func (c *Conn) SendWithTimeout(method string, params map[string]any, timeout time.Duration) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	result, err := c.SendContext(ctx, method, params)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("CDP %s: timeout after %v: %w", method, timeout, err)
	}
	return result, err
}

// SendContext sends a CDP command and waits for its response within ctx.
func (c *Conn) SendContext(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	if ctx == nil {
		return nil, fmt.Errorf("CDP %s: nil context", method)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("CDP %s: %w", method, err)
	}

	id := c.nextID.Add(1)
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

	responseCh := make(chan commandResponse, 1)
	c.mu.Lock()
	if c.terminalErr != nil {
		err := c.terminalErr
		c.mu.Unlock()
		return nil, fmt.Errorf("CDP %s: %w", method, err)
	}
	c.pending[id] = responseCh
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.ws.Write(ctx, websocket.MessageText, data); err != nil {
		connectionErr := fmt.Errorf("CDP connection write failed: %w", err)
		c.terminate(connectionErr)
		return nil, fmt.Errorf("CDP %s: %w", method, connectionErr)
	}

	select {
	case response := <-responseCh:
		if response.err != nil {
			return nil, fmt.Errorf("CDP %s: %w", method, response.err)
		}
		return response.result, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("CDP %s: %w", method, ctx.Err())
	}
}

// Close closes the WebSocket connection. It is safe to call concurrently and
// repeatedly.
func (c *Conn) Close() error {
	return c.close(true)
}

func (c *Conn) closeNow() error {
	return c.close(false)
}

func (c *Conn) close(graceful bool) error {
	c.terminate(errConnectionClosed)
	c.wsCloseOnce.Do(func() {
		if graceful {
			c.wsCloseErr = c.ws.Close(websocket.StatusNormalClosure, "closing")
			return
		}
		c.wsCloseErr = c.ws.CloseNow()
	})
	return c.wsCloseErr
}

// terminate atomically marks the connection unusable and delivers the same
// terminal error to every command that was pending at that instant.
func (c *Conn) terminate(err error) {
	if err == nil {
		err = errConnectionClosed
	}
	c.terminateOnce.Do(func() {
		c.mu.Lock()
		c.terminalErr = err
		pending := c.pending
		c.pending = make(map[int64]chan commandResponse)
		close(c.done)
		c.mu.Unlock()

		for _, responseCh := range pending {
			responseCh <- commandResponse{err: err}
		}
	})
}

// readLoop reads messages from the WebSocket and dispatches responses.
func (c *Conn) readLoop() {
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			select {
			case <-c.done:
				return
			default:
				c.terminate(fmt.Errorf("CDP connection read failed: %w", err))
				_ = c.closeNow()
				return
			}
		}

		var msg cdpMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.terminate(fmt.Errorf("decode CDP message: %w", err))
			_ = c.closeNow()
			return
		}

		if msg.ID == 0 {
			continue // Event; callers currently only wait for command responses.
		}

		c.mu.Lock()
		responseCh, ok := c.pending[msg.ID]
		if ok {
			delete(c.pending, msg.ID)
		}
		c.mu.Unlock()
		if !ok {
			continue
		}

		if msg.Error != nil {
			responseCh <- commandResponse{err: msg.Error}
			continue
		}
		responseCh <- commandResponse{result: msg.Result}
	}
}

func connectWS(ctx context.Context, wsURL string) (*Conn, error) {
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("websocket dial %s: %w", wsURL, err)
	}
	// CDP evaluation results can exceed the library's default 32 KiB limit.
	ws.SetReadLimit(-1)

	conn := &Conn{
		ws:      ws,
		pending: make(map[int64]chan commandResponse),
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

func discoverWSURL(ctx context.Context, port int) (string, error) {
	if ctx == nil {
		return "", errors.New("discover CDP targets: nil context")
	}
	if err := validateLocalPort(port); err != nil {
		return "", err
	}
	url := fmt.Sprintf("http://localhost:%d/json/list", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create CDP target request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("discover CDP targets at %s: %w", url, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("discover CDP targets at %s: unexpected HTTP status %s", url, resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTargetListSize+1))
	if err != nil {
		return "", fmt.Errorf("read targets response: %w", err)
	}
	if len(body) > maxTargetListSize {
		return "", fmt.Errorf("targets response exceeded %d bytes", maxTargetListSize)
	}

	var targets []target
	if err := json.Unmarshal(body, &targets); err != nil {
		return "", fmt.Errorf("parse targets: %w", err)
	}
	if len(targets) == 0 {
		return "", fmt.Errorf("no CDP targets found at %s", url)
	}

	for _, target := range targets {
		if target.Type == "page" && target.WebSocketDebuggerURL != "" {
			return target.WebSocketDebuggerURL, nil
		}
	}
	for _, target := range targets {
		if target.WebSocketDebuggerURL != "" {
			return target.WebSocketDebuggerURL, nil
		}
	}
	return "", fmt.Errorf("no targets with webSocketDebuggerUrl at %s", url)
}

func waitForRetry(ctx context.Context) error {
	timer := time.NewTimer(connectRetryInterval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func retryError(action string, contextErr, lastErr error) error {
	if lastErr == nil {
		return fmt.Errorf("%s: %w", action, contextErr)
	}
	return fmt.Errorf("%s: %w (last error: %v)", action, contextErr, lastErr)
}

func validateLocalPort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid local port %d: must be between 1 and 65535", port)
	}
	return nil
}
