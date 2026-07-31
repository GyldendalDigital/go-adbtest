package cdp

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/adb"
)

// Client provides high-level WebView interactions over CDP.
type Client struct {
	Conn       *Conn
	ADB        *adb.Client
	LocalPort  int
	AppPackage string
}

// NewClient creates a CDP client, sets up port forwarding, and connects.
func NewClient(adbClient *adb.Client, appPackage string, localPort int) (*Client, error) {
	pid, err := appPID(adbClient, appPackage)
	if err != nil {
		return nil, err
	}

	socketName := fmt.Sprintf("webview_devtools_remote_%d", pid)
	if err := adbClient.Forward(localPort, socketName); err != nil {
		return nil, fmt.Errorf("port forward: %w", err)
	}

	conn, err := ConnectWithRetry(localPort, 30*time.Second)
	if err != nil {
		_ = adbClient.RemoveForward(localPort)
		return nil, err
	}

	return &Client{
		Conn:       conn,
		ADB:        adbClient,
		LocalPort:  localPort,
		AppPackage: appPackage,
	}, nil
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
	js := fmt.Sprintf(`document.querySelector(%q) !== null`, cssSelector)
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

	_ = c.Conn.Close()
	_ = c.ADB.RemoveForward(c.LocalPort)

	pid, err := appPID(c.ADB, c.AppPackage)
	if err != nil {
		t.Fatalf("CDP Reconnect: get PID: %v", err)
	}

	socketName := fmt.Sprintf("webview_devtools_remote_%d", pid)
	if err := c.ADB.Forward(c.LocalPort, socketName); err != nil {
		t.Fatalf("CDP Reconnect: forward: %v", err)
	}

	conn, err := ConnectWithRetry(c.LocalPort, 30*time.Second)
	if err != nil {
		t.Fatalf("CDP Reconnect: connect: %v", err)
	}
	c.Conn = conn
}

// Close closes the CDP connection and removes port forwarding.
func (c *Client) Close() error {
	err := c.Conn.Close()
	_ = c.ADB.RemoveForward(c.LocalPort)
	return err
}

func appPID(adbClient *adb.Client, pkg string) (int, error) {
	out, err := adbClient.Shell("pidof " + pkg)
	if err != nil {
		return 0, fmt.Errorf("get PID of %s: %w", pkg, err)
	}
	var pid int
	if _, err := fmt.Sscanf(out, "%d", &pid); err != nil {
		return 0, fmt.Errorf("parse PID %q: %w", out, err)
	}
	return pid, nil
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
