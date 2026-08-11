// Package cdp connects to an Android WebView's Chrome DevTools Protocol
// endpoint and provides test-oriented DOM and JavaScript interactions.
package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/adb"
)

const (
	clientReadyTimeout   = 30 * time.Second
	clientCommandTimeout = 30 * time.Second
	clientCloseTimeout   = 2 * time.Second
	clientPollInterval   = 200 * time.Millisecond
)

// Client provides high-level WebView interactions over CDP.
type Client struct {
	// Conn is the active CDP WebSocket connection.
	Conn *Conn
	// ADB is the device client used to discover the app and manage forwarding.
	ADB *adb.Client
	// LocalPort is the host TCP port forwarded to the WebView DevTools socket.
	LocalPort int
	// AppPackage is the Android application package whose WebView is targeted.
	AppPackage string
	// AppProcess is the Android process whose WebView is targeted. It defaults
	// to AppPackage; secondary processes use names such as
	// "com.example.app:webview".
	AppProcess string

	forwardOwned  bool
	forwardSocket string
}

// RecoveryPolicy controls bounded recovery of a polling operation after CDP
// transport loss. MaxReconnects == 0 uses the default of one reconnect; use
// a negative MaxReconnects to disable recovery.
type RecoveryPolicy struct {
	MaxReconnects int
}

const defaultMaxReconnects = 1

func (p RecoveryPolicy) maxReconnects() int {
	if p.MaxReconnects < 0 {
		return 0
	}
	if p.MaxReconnects == 0 {
		return defaultMaxReconnects
	}
	return p.MaxReconnects
}

// NewClient creates a CDP client, sets up port forwarding, and connects.
func NewClient(adbClient *adb.Client, appPackage string, localPort int) (*Client, error) {
	return NewClientForProcess(adbClient, appPackage, appPackage, localPort)
}

// NewClientForProcess creates a CDP client for an explicit Android app
// process. An empty appProcess targets the package's default process.
func NewClientForProcess(adbClient *adb.Client, appPackage, appProcess string, localPort int) (*Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), clientReadyTimeout)
	defer cancel()
	return NewClientForProcessContext(ctx, adbClient, appPackage, appProcess, localPort)
}

// NewClientContext creates a CDP client, polling app PID and WebView readiness
// while honoring one context across ADB forwarding, discovery, and dialing.
func NewClientContext(ctx context.Context, adbClient *adb.Client, appPackage string, localPort int) (*Client, error) {
	return NewClientForProcessContext(ctx, adbClient, appPackage, appPackage, localPort)
}

// NewClientForProcessContext creates a CDP client for an explicit Android app
// process while honoring one context across discovery, forwarding, and dialing.
// An empty appProcess targets the package's default process.
func NewClientForProcessContext(
	ctx context.Context,
	adbClient *adb.Client,
	appPackage, appProcess string,
	localPort int,
) (*Client, error) {
	if ctx == nil {
		return nil, fmt.Errorf("create CDP client: nil context")
	}
	if adbClient == nil {
		return nil, fmt.Errorf("create CDP client: nil ADB client")
	}
	if err := validateAppPackage(appPackage); err != nil {
		return nil, err
	}
	appProcess, err := normalizeAppProcess(appPackage, appProcess)
	if err != nil {
		return nil, err
	}
	if err := validateLocalPort(localPort); err != nil {
		return nil, err
	}
	client := &Client{
		ADB:        adbClient,
		LocalPort:  localPort,
		AppPackage: appPackage,
		AppProcess: appProcess,
	}

	conn, err := client.connectContext(ctx)
	if err != nil {
		return nil, err
	}
	client.Conn = conn
	return client, nil
}

// Eval evaluates a JS expression. String values are returned directly;
// non-string by-value results are returned as JSON text.
func (c *Client) Eval(t testing.TB, expression string) string {
	t.Helper()
	result, err := c.EvalE(expression)
	if err != nil {
		t.Fatalf("CDP Eval(%q): %v", expression, err)
	}
	return result
}

// EvalE evaluates a JS expression with a 30-second command timeout. String
// values are returned directly; non-string by-value results are JSON text.
func (c *Client) EvalE(expression string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), clientCommandTimeout)
	defer cancel()
	return c.EvalContext(ctx, expression)
}

// EvalContext evaluates a JS expression and returns its by-value result while
// bounding the CDP command by ctx.
func (c *Client) EvalContext(ctx context.Context, expression string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("CDP Eval: nil context")
	}
	conn, err := c.activeConn()
	if err != nil {
		return "", err
	}
	resp, err := conn.SendContext(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
	})
	if err != nil {
		return "", err
	}
	return extractValue(resp)
}

// EvalAsync evaluates and awaits a JS Promise with a 60-second command timeout.
func (c *Client) EvalAsync(t testing.TB, expression string) string {
	t.Helper()
	conn, err := c.activeConn()
	if err != nil {
		t.Fatalf("CDP EvalAsync(%q): %v", expression, err)
		return ""
	}
	resp, err := conn.SendWithTimeout("Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	}, 60*time.Second)
	if err != nil {
		t.Fatalf("CDP EvalAsync(%q): %v", expression, err)
	}
	val, err := extractValue(resp)
	if err != nil {
		t.Fatalf("CDP EvalAsync(%q): %v", expression, err)
	}
	return val
}

// Click dispatches mousePressed+mouseReleased at the center of a CSS-selected element.
// Uses Input.dispatchMouseEvent for a real user gesture.
func (c *Client) Click(t testing.TB, cssSelector string) {
	t.Helper()
	conn, err := c.activeConn()
	if err != nil {
		t.Fatalf("CDP Click(%q): %v", cssSelector, err)
		return
	}
	// Get bounding rect of the element
	js := fmt.Sprintf(`JSON.stringify(document.querySelector(%q).getBoundingClientRect())`, cssSelector)
	rectJSON := c.Eval(t, js)

	var rect struct {
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	}
	if err := json.Unmarshal([]byte(rectJSON), &rect); err != nil {
		t.Fatalf("CDP Click(%q): parse rect: %v", cssSelector, err)
	}

	cx := rect.X + rect.Width/2
	cy := rect.Y + rect.Height/2

	// mousePressed
	if _, err := conn.Send("Input.dispatchMouseEvent", map[string]any{
		"type":       "mousePressed",
		"x":          cx,
		"y":          cy,
		"button":     "left",
		"clickCount": 1,
	}); err != nil {
		t.Fatalf("CDP Click(%q): mousePressed: %v", cssSelector, err)
	}

	// mouseReleased
	if _, err := conn.Send("Input.dispatchMouseEvent", map[string]any{
		"type":       "mouseReleased",
		"x":          cx,
		"y":          cy,
		"button":     "left",
		"clickCount": 1,
	}); err != nil {
		t.Fatalf("CDP Click(%q): mouseReleased: %v", cssSelector, err)
	}
}

// WaitForSelector polls until a CSS selector matches a visible element.
func (c *Client) WaitForSelector(t testing.TB, cssSelector string, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := c.WaitForSelectorContext(ctx, cssSelector, RecoveryPolicy{}); err != nil {
		t.Fatalf("WaitForSelector(%q): timeout after %v: %v", cssSelector, timeout, err)
	}
}

func (c *Client) waitForSelectorContext(ctx context.Context, cssSelector string) error {
	return c.WaitForSelectorContext(ctx, cssSelector, RecoveryPolicy{})
}

// WaitForSelectorContext polls until a CSS selector matches a visible
// element. Transport recovery follows policy (one reconnect for its zero
// value), and the context's original deadline is preserved.
func (c *Client) WaitForSelectorContext(ctx context.Context, cssSelector string, policy RecoveryPolicy) error {
	if ctx == nil {
		return fmt.Errorf("wait for selector %q: nil context", cssSelector)
	}
	js := visibleSelectorExpression(cssSelector)
	reconnects := 0
	for {
		result, err := c.EvalContext(ctx, js)
		if err != nil {
			if IsTransportError(err) && reconnects < policy.maxReconnects() && c != nil && c.ADB != nil {
				reconnects++
				reconnectErr := c.ReconnectContext(ctx)
				if reconnectErr == nil {
					continue
				}
				return fmt.Errorf("recover selector %q after transport failure: %w", cssSelector, errors.Join(err, reconnectErr))
			}
			return fmt.Errorf("evaluate selector %q: %w", cssSelector, err)
		}
		if result == "true" {
			return nil
		}
		if err := waitForClientPoll(ctx); err != nil {
			return fmt.Errorf("wait for selector %q: %w", cssSelector, err)
		}
	}
}

func visibleSelectorExpression(cssSelector string) string {
	return fmt.Sprintf(`(() => {
  const element = document.querySelector(%q);
  if (!element) return false;
  for (let current = element; current; current = current.parentElement) {
    const style = getComputedStyle(current);
    if (style.display === "none" || style.visibility === "hidden" ||
        style.visibility === "collapse" || Number.parseFloat(style.opacity) === 0) {
      return false;
    }
  }
  const rect = element.getBoundingClientRect();
  return rect.width > 0 && rect.height > 0;
})()`, cssSelector)
}

// WaitForText polls until an element's textContent contains the substring.
func (c *Client) WaitForText(t testing.TB, cssSelector, text string, timeout time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result, err := c.WaitForTextContext(ctx, cssSelector, text, RecoveryPolicy{})
	if err != nil {
		t.Fatalf("WaitForText(%q, %q): timeout after %v: %v", cssSelector, text, timeout, err)
		return ""
	}
	return result
}

func (c *Client) waitForTextContext(ctx context.Context, cssSelector, text string) (string, error) {
	return c.WaitForTextContext(ctx, cssSelector, text, RecoveryPolicy{})
}

// WaitForTextContext polls until an element's textContent contains text.
// Transport recovery is bounded by policy and shares ctx's original deadline.
func (c *Client) WaitForTextContext(ctx context.Context, cssSelector, text string, policy RecoveryPolicy) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("wait for text on selector %q: nil context", cssSelector)
	}
	js := fmt.Sprintf(`document.querySelector(%q)?.textContent ?? ""`, cssSelector)
	var lastContent string
	reconnects := 0
	for {
		result, err := c.EvalContext(ctx, js)
		if err != nil {
			if IsTransportError(err) && reconnects < policy.maxReconnects() && c != nil && c.ADB != nil {
				reconnects++
				reconnectErr := c.ReconnectContext(ctx)
				if reconnectErr == nil {
					continue
				}
				return "", fmt.Errorf("recover text %q on selector %q after transport failure: %w", text, cssSelector, errors.Join(err, reconnectErr))
			}
			return "", fmt.Errorf("evaluate text for selector %q: %w", cssSelector, err)
		}
		lastContent = result
		if contains(result, text) {
			return result, nil
		}
		if err := waitForClientPoll(ctx); err != nil {
			return "", fmt.Errorf("wait for text %q on selector %q (last content: %q): %w", text, cssSelector, lastContent, err)
		}
	}
}

// Reconnect re-discovers the app PID, re-forwards the port, and reconnects.
func (c *Client) Reconnect(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), clientReadyTimeout)
	defer cancel()
	if err := c.ReconnectContext(ctx); err != nil {
		t.Fatalf("CDP Reconnect: %v", err)
	}
}

// ReconnectContext closes the old connection, then polls the app PID and
// WebView readiness while honoring ctx.
func (c *Client) ReconnectContext(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("reconnect CDP client: nil client")
	}
	if ctx == nil {
		return fmt.Errorf("reconnect CDP client: nil context")
	}
	if c.ADB == nil {
		return fmt.Errorf("reconnect CDP client: nil ADB client")
	}
	if err := validateAppPackage(c.AppPackage); err != nil {
		return err
	}
	appProcess, err := normalizeAppProcess(c.AppPackage, c.AppProcess)
	if err != nil {
		return err
	}
	c.AppProcess = appProcess
	if err := validateLocalPort(c.LocalPort); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("reconnect CDP client: %w", err)
	}

	oldConn := c.Conn
	c.Conn = nil
	var closeErr error
	if oldConn != nil {
		closeErr = oldConn.closeNow()
	}
	removeErr := c.removeForwardContext(ctx)
	if err := errors.Join(closeErr, removeErr); err != nil {
		return fmt.Errorf("prepare CDP reconnect: %w", err)
	}

	conn, err := c.connectContext(ctx)
	if err != nil {
		return err
	}
	c.Conn = conn
	return nil
}

// Close closes the CDP connection and removes port forwarding.
func (c *Client) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), clientCloseTimeout)
	defer cancel()
	return c.CloseContext(ctx)
}

// CloseContext immediately closes the CDP connection and removes its port
// forward within ctx. It is safe on nil and partially initialized clients.
func (c *Client) CloseContext(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("close CDP client: nil context")
	}

	conn := c.Conn
	c.Conn = nil
	var closeErr error
	if conn != nil {
		closeErr = conn.closeNow()
	}

	var removeErr error
	if c.forwardOwned {
		if err := validateLocalPort(c.LocalPort); err != nil {
			removeErr = err
		} else if c.ADB == nil {
			removeErr = fmt.Errorf("remove CDP port forward: nil ADB client")
		} else {
			removeErr = c.removeForwardContext(ctx)
		}
	}
	return errors.Join(closeErr, removeErr)
}

func (c *Client) connectContext(ctx context.Context) (conn *Conn, err error) {
	var lastErr error
	defer func() {
		if err == nil || !c.forwardOwned {
			return
		}
		if cleanupErr := c.cleanupForward(); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()

	appProcess, err := normalizeAppProcess(c.AppPackage, c.AppProcess)
	if err != nil {
		return nil, err
	}
	c.AppProcess = appProcess

	for {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, retryError("connect CDP client", contextErr, lastErr)
		}

		socketName, attemptErr := webViewSocketContext(ctx, c.ADB, c.AppProcess)
		if attemptErr == nil {
			if attemptErr = c.ensureForwardContext(ctx, socketName); attemptErr != nil {
				return nil, fmt.Errorf("connect CDP client: %w", attemptErr)
			}
			conn, attemptErr = ConnectContext(ctx, c.LocalPort)
			if attemptErr == nil {
				return conn, nil
			}
		} else {
			var ambiguity *ambiguousWebViewSocketError
			if errors.As(attemptErr, &ambiguity) {
				return nil, fmt.Errorf("connect CDP client: %w", attemptErr)
			}
		}
		lastErr = attemptErr

		if retryErr := waitForRetry(ctx); retryErr != nil {
			return nil, retryError("connect CDP client", retryErr, lastErr)
		}
	}
}

func (c *Client) ensureForwardContext(ctx context.Context, socketName string) error {
	if c.forwardOwned {
		if c.forwardSocket == socketName {
			return nil
		}
		if err := c.removeForwardContext(ctx); err != nil {
			return err
		}
	}

	_, err := c.ADB.RunContext(
		ctx,
		"forward",
		"--no-rebind",
		fmt.Sprintf("tcp:%d", c.LocalPort),
		"localabstract:"+socketName,
	)
	if err != nil {
		return fmt.Errorf("create exclusive CDP port forward: %w", err)
	}
	c.forwardOwned = true
	c.forwardSocket = socketName
	return nil
}

func (c *Client) removeForwardContext(ctx context.Context) error {
	if c == nil || !c.forwardOwned {
		return nil
	}
	if c.ADB == nil {
		return fmt.Errorf("remove CDP port forward: nil ADB client")
	}
	_, err := c.ADB.RunContext(ctx, "forward", "--remove", fmt.Sprintf("tcp:%d", c.LocalPort))
	if err != nil {
		return fmt.Errorf("remove CDP port forward: %w", err)
	}
	c.forwardOwned = false
	c.forwardSocket = ""
	return nil
}

func (c *Client) cleanupForward() error {
	ctx, cancel := context.WithTimeout(context.Background(), clientCloseTimeout)
	defer cancel()
	return c.removeForwardContext(ctx)
}

func validateAppPackage(appPackage string) error {
	if appPackage == "" {
		return fmt.Errorf("app package is required")
	}
	if strings.HasPrefix(appPackage, ".") || strings.HasSuffix(appPackage, ".") || strings.Contains(appPackage, "..") {
		return fmt.Errorf("invalid app package %q", appPackage)
	}
	for _, char := range appPackage {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '.' {
			continue
		}
		return fmt.Errorf("invalid app package %q", appPackage)
	}
	return nil
}

// extractValue extracts a string representation from a Runtime.evaluate
// response result.
func extractValue(resp json.RawMessage) (string, error) {
	var result struct {
		Result *struct {
			Value               json.RawMessage `json:"value"`
			Type                string          `json:"type"`
			UnserializableValue string          `json:"unserializableValue"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception *struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", fmt.Errorf("parse eval result: %w", err)
	}
	if result.ExceptionDetails != nil {
		message := result.ExceptionDetails.Text
		if message == "" && result.ExceptionDetails.Exception != nil {
			message = result.ExceptionDetails.Exception.Description
		}
		if message == "" {
			message = "runtime evaluation failed"
		}
		return "", fmt.Errorf("JS exception: %s", message)
	}
	if result.Result == nil {
		return "", fmt.Errorf("parse eval result: response did not contain result")
	}
	if result.Result.Type == "" {
		return "", fmt.Errorf("parse eval result: remote object did not contain type")
	}
	if result.Result.UnserializableValue != "" {
		return result.Result.UnserializableValue, nil
	}
	if result.Result.Type == "undefined" {
		if len(result.Result.Value) != 0 {
			return "", fmt.Errorf("parse eval result: undefined remote object unexpectedly contained value")
		}
		return "undefined", nil
	}
	if len(result.Result.Value) == 0 {
		return "", fmt.Errorf("parse eval result: remote object type %q did not contain a by-value result", result.Result.Type)
	}
	if !json.Valid(result.Result.Value) {
		return "", fmt.Errorf("parse eval result: remote object value is not valid JSON")
	}
	if result.Result.Type == "string" {
		var value *string
		if err := json.Unmarshal(result.Result.Value, &value); err != nil || value == nil {
			return "", fmt.Errorf("parse eval result: invalid string value %s", result.Result.Value)
		}
		return *value, nil
	}
	return string(result.Result.Value), nil
}

func (c *Client) activeConn() (*Conn, error) {
	if c == nil {
		return nil, fmt.Errorf("CDP client is nil")
	}
	if c.Conn == nil {
		return nil, fmt.Errorf("CDP client is not connected")
	}
	return c.Conn, nil
}

func waitForClientPoll(ctx context.Context) error {
	timer := time.NewTimer(clientPollInterval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func contains(s, substr string) bool {
	return substr != "" && len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
