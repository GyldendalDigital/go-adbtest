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
	clientReadyTimeout = 30 * time.Second
	clientCloseTimeout = 2 * time.Second
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
}

// NewClient creates a CDP client, sets up port forwarding, and connects.
func NewClient(adbClient *adb.Client, appPackage string, localPort int) (*Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), clientReadyTimeout)
	defer cancel()
	return NewClientContext(ctx, adbClient, appPackage, localPort)
}

// NewClientContext creates a CDP client, polling app PID and WebView readiness
// while honoring one context across ADB forwarding, discovery, and dialing.
func NewClientContext(ctx context.Context, adbClient *adb.Client, appPackage string, localPort int) (*Client, error) {
	if ctx == nil {
		return nil, fmt.Errorf("create CDP client: nil context")
	}
	if adbClient == nil {
		return nil, fmt.Errorf("create CDP client: nil ADB client")
	}
	if err := validateAppPackage(appPackage); err != nil {
		return nil, err
	}
	if err := validateLocalPort(localPort); err != nil {
		return nil, err
	}
	client := &Client{
		ADB:        adbClient,
		LocalPort:  localPort,
		AppPackage: appPackage,
	}

	conn, err := client.connectContext(ctx)
	if err != nil {
		return nil, err
	}
	client.Conn = conn
	return client, nil
}

// Eval evaluates a JS expression and returns the string result.
func (c *Client) Eval(t testing.TB, expression string) string {
	t.Helper()
	result, err := c.EvalE(expression)
	if err != nil {
		t.Fatalf("CDP Eval(%q): %v", expression, err)
	}
	return result
}

// EvalE evaluates a JS expression and returns the result or error.
func (c *Client) EvalE(expression string) (string, error) {
	resp, err := c.Conn.Send("Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
	})
	if err != nil {
		return "", err
	}
	return extractValue(resp)
}

// EvalAsync evaluates a JS expression that returns a Promise, awaiting it.
func (c *Client) EvalAsync(t testing.TB, expression string) string {
	t.Helper()
	resp, err := c.Conn.SendWithTimeout("Runtime.evaluate", map[string]any{
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
	if _, err := c.Conn.Send("Input.dispatchMouseEvent", map[string]any{
		"type":       "mousePressed",
		"x":          cx,
		"y":          cy,
		"button":     "left",
		"clickCount": 1,
	}); err != nil {
		t.Fatalf("CDP Click(%q): mousePressed: %v", cssSelector, err)
	}

	// mouseReleased
	if _, err := c.Conn.Send("Input.dispatchMouseEvent", map[string]any{
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
	js := visibleSelectorExpression(cssSelector)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result, err := c.EvalE(js)
		if err == nil && result == "true" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("WaitForSelector(%q): timeout after %v", cssSelector, timeout)
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
func (c *Client) WaitForText(t testing.TB, cssSelector string, text string, timeout time.Duration) string {
	t.Helper()
	js := fmt.Sprintf(`document.querySelector(%q)?.textContent ?? ""`, cssSelector)
	deadline := time.Now().Add(timeout)
	var lastContent string
	for time.Now().Before(deadline) {
		result, err := c.EvalE(js)
		if err == nil {
			lastContent = result
			if contains(result, text) {
				return result
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("WaitForText(%q, %q): timeout after %v, last content: %q", cssSelector, text, timeout, lastContent)
	return ""
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
	if ctx == nil {
		return fmt.Errorf("reconnect CDP client: nil context")
	}
	if c.ADB == nil {
		return fmt.Errorf("reconnect CDP client: nil ADB client")
	}
	if err := validateAppPackage(c.AppPackage); err != nil {
		return err
	}
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
	if c.ADB != nil {
		if err := validateLocalPort(c.LocalPort); err != nil {
			removeErr = err
		} else {
			removeErr = c.removeForwardContext(ctx)
		}
	}
	return errors.Join(closeErr, removeErr)
}

func appPIDContext(ctx context.Context, adbClient *adb.Client, pkg string) (int, error) {
	if err := validateAppPackage(pkg); err != nil {
		return 0, err
	}
	out, err := adbClient.ShellContext(ctx, "pidof "+pkg)
	if err != nil {
		return 0, fmt.Errorf("get PID of %s: %w", pkg, err)
	}
	var pid int
	if _, err := fmt.Sscanf(out, "%d", &pid); err != nil {
		return 0, fmt.Errorf("parse PID %q: %w", out, err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("parse PID %q: PID must be positive", out)
	}
	return pid, nil
}

func (c *Client) connectContext(ctx context.Context) (conn *Conn, err error) {
	var lastErr error
	forwarded := false
	defer func() {
		if err == nil || !forwarded {
			return
		}
		if cleanupErr := c.cleanupForward(); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()

	for {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, retryError("connect CDP client", contextErr, lastErr)
		}

		pid, attemptErr := appPIDContext(ctx, c.ADB, c.AppPackage)
		if attemptErr == nil {
			socketName := fmt.Sprintf("webview_devtools_remote_%d", pid)
			_, attemptErr = c.ADB.RunContext(
				ctx,
				"forward",
				fmt.Sprintf("tcp:%d", c.LocalPort),
				"localabstract:"+socketName,
			)
			if attemptErr == nil {
				forwarded = true
				conn, attemptErr = ConnectContext(ctx, c.LocalPort)
				if attemptErr == nil {
					return conn, nil
				}
			}
		}
		lastErr = attemptErr

		if retryErr := waitForRetry(ctx); retryErr != nil {
			return nil, retryError("connect CDP client", retryErr, lastErr)
		}
	}
}

func (c *Client) removeForwardContext(ctx context.Context) error {
	_, err := c.ADB.RunContext(ctx, "forward", "--remove", fmt.Sprintf("tcp:%d", c.LocalPort))
	if err != nil {
		return fmt.Errorf("remove CDP port forward: %w", err)
	}
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

// extractValue extracts the string value from a Runtime.evaluate response.
func extractValue(resp json.RawMessage) (string, error) {
	var result struct {
		Result struct {
			Value json.RawMessage `json:"value"`
			Type  string          `json:"type"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", fmt.Errorf("parse eval result: %w", err)
	}
	if result.ExceptionDetails != nil {
		return "", fmt.Errorf("JS exception: %s", result.ExceptionDetails.Text)
	}

	// Return the value as a string
	var s string
	if err := json.Unmarshal(result.Result.Value, &s); err != nil {
		// Not a string — return the raw JSON
		return string(result.Result.Value), nil
	}
	return s, nil
}

func contains(s, substr string) bool {
	return len(substr) > 0 && len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
