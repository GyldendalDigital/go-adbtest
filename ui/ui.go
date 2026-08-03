package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/adb"
)

const (
	defaultTimeout  = 10 * time.Second
	pollInterval    = 500 * time.Millisecond
	systemUIANRText = "System UI isn't responding"
)

// Interactor provides native UI interactions via uiautomator + adb input.
type Interactor struct {
	// ADB is the device client used for hierarchy dumps and input commands.
	ADB *adb.Client
	// Timeout is the default wait timeout used when a helper has no override.
	Timeout time.Duration
}

// NewInteractor creates an Interactor with the given ADB client.
func NewInteractor(adbClient *adb.Client) *Interactor {
	return &Interactor{
		ADB:     adbClient,
		Timeout: defaultTimeout,
	}
}

// TapOnText finds an element by case-sensitive text substring and taps its
// center. It polls until the first positive timeout override, or Timeout.
func (u *Interactor) TapOnText(t testing.TB, text string, timeout ...time.Duration) {
	t.Helper()
	to := u.resolveTimeout(timeout)
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()
	el, err := u.waitForElementContext(ctx, text, to)
	if err != nil {
		t.Fatalf("TapOnText(%q): %v", text, err)
	}
	u.tapElementContext(t, ctx, &el)
}

// LongPressOnText finds an element by case-sensitive text substring and
// long-presses it with a 1.5-second swipe-in-place gesture.
func (u *Interactor) LongPressOnText(t testing.TB, text string, timeout ...time.Duration) {
	t.Helper()
	to := u.resolveTimeout(timeout)
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()
	el, err := u.waitForElementContext(ctx, text, to)
	if err != nil {
		t.Fatalf("LongPressOnText(%q): %v", text, err)
	}
	cx, cy := el.Bounds.CenterX(), el.Bounds.CenterY()
	client, err := u.adbClient()
	if err == nil {
		_, err = client.ShellContext(ctx, fmt.Sprintf("input swipe %d %d %d %d 1500", cx, cy, cx, cy))
	}
	if err != nil {
		t.Fatalf("LongPressOnText(%q): input swipe: %v", text, err)
	}
}

// TapOnID finds an element by resource-id and taps its center.
func (u *Interactor) TapOnID(t testing.TB, resourceID string, timeout ...time.Duration) {
	t.Helper()
	to := u.resolveTimeout(timeout)
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()
	el, err := u.waitForElementByIDContext(ctx, resourceID, to)
	if err != nil {
		t.Fatalf("TapOnID(%q): %v", resourceID, err)
	}
	u.tapElementContext(t, ctx, &el)
}

// WaitForText polls until an element containing the case-sensitive text
// substring appears.
func (u *Interactor) WaitForText(t testing.TB, text string, timeout ...time.Duration) {
	t.Helper()
	to := u.resolveTimeout(timeout)
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()
	if _, err := u.waitForElementContext(ctx, text, to); err != nil {
		t.Fatalf("WaitForText(%q): %v", text, err)
	}
}

// AssertVisible asserts that a case-sensitive text substring is currently
// visible in the UI hierarchy.
func (u *Interactor) AssertVisible(t testing.TB, text string) {
	t.Helper()
	elements, err := u.Dump()
	if err != nil {
		t.Fatalf("AssertVisible(%q): dump failed: %v", text, err)
	}
	if len(FindByText(elements, text)) == 0 {
		t.Fatalf("AssertVisible(%q): text not visible. Visible: %s", text, visibleTexts(elements))
	}
}

// AssertGone asserts that a case-sensitive text substring is not currently
// visible in the UI hierarchy.
func (u *Interactor) AssertGone(t testing.TB, text string) {
	t.Helper()
	elements, err := u.Dump()
	if err != nil {
		t.Fatalf("AssertGone(%q): dump failed: %v", text, err)
	}
	if len(FindByText(elements, text)) > 0 {
		t.Fatalf("AssertGone(%q): text is still visible", text)
	}
}

// Dump returns the current UI hierarchy as parsed Elements.
func (u *Interactor) Dump() ([]Element, error) {
	ctx, cancel := context.WithTimeout(context.Background(), u.operationTimeout())
	defer cancel()
	return u.DumpContext(ctx)
}

// DumpContext returns the current UI hierarchy while bounding every ADB
// command, including fallback pulling and remote cleanup, by ctx.
func (u *Interactor) DumpContext(ctx context.Context) ([]Element, error) {
	if ctx == nil {
		return nil, fmt.Errorf("dump UI hierarchy: nil context")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("dump UI hierarchy: %w", err)
	}
	client, err := u.adbClient()
	if err != nil {
		return nil, err
	}

	// Try fast path: dump to stdout via /dev/tty
	out, err := client.ShellContext(ctx, "uiautomator dump /dev/tty")
	var fastPathErr error
	if err == nil {
		xmlData, extractErr := hierarchyXML(out)
		if extractErr == nil {
			elements, parseErr := ParseDump(xmlData)
			if parseErr == nil {
				return elements, nil
			}
			fastPathErr = parseErr
		} else {
			fastPathErr = extractErr
		}
	} else {
		fastPathErr = err
	}

	// Fallback: use one unique device file so concurrent or failed dumps cannot
	// consume an earlier hierarchy.
	tmpDir, err := os.MkdirTemp("", "go-adbtest-ui-")
	if err != nil {
		return nil, fmt.Errorf("create temporary directory for ui dump: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	remotePath := "/data/local/tmp/" + filepath.Base(tmpDir) + ".xml"
	if _, err := client.ShellContext(ctx, "rm -f "+remotePath); err != nil {
		return nil, fmt.Errorf("remove previous ui dump path: %w (fast path: %v)", err, fastPathErr)
	}
	defer func() {
		_, _ = client.ShellContext(ctx, "rm -f "+remotePath)
	}()
	if _, err := client.ShellContext(ctx, "uiautomator dump "+remotePath); err != nil {
		return nil, fmt.Errorf("uiautomator fallback dump: %w (fast path: %v)", err, fastPathErr)
	}

	localPath := filepath.Join(tmpDir, "ui.xml")
	if _, err := client.RunContext(ctx, "pull", remotePath, localPath); err != nil {
		return nil, fmt.Errorf("pull ui dump: %w", err)
	}
	//nolint:gosec // localPath is generated inside the private temporary directory.
	xmlData, err := os.ReadFile(localPath)
	if err != nil {
		return nil, fmt.Errorf("read pulled ui dump: %w", err)
	}

	elements, err := ParseDump(xmlData)
	if err != nil {
		return nil, fmt.Errorf("parse pulled ui dump: %w", err)
	}
	return elements, nil
}

// hierarchyXML extracts exactly one uiautomator hierarchy document. The
// /dev/tty form normally appends a human-readable status line after the XML.
func hierarchyXML(out string) ([]byte, error) {
	start := strings.Index(out, "<hierarchy")
	if start < 0 {
		return nil, fmt.Errorf("ui dump did not contain <hierarchy>")
	}

	const closingTag = "</hierarchy>"
	endOffset := strings.Index(out[start:], closingTag)
	if endOffset < 0 {
		return nil, fmt.Errorf("ui dump did not contain %s", closingTag)
	}
	end := start + endOffset + len(closingTag)
	return []byte(out[start:end]), nil
}

// Tap sends an input tap at absolute coordinates.
func (u *Interactor) Tap(x, y int) error {
	ctx, cancel := context.WithTimeout(context.Background(), u.operationTimeout())
	defer cancel()
	return u.TapContext(ctx, x, y)
}

// TapContext sends an input tap at absolute coordinates while bounding the ADB
// command by ctx.
func (u *Interactor) TapContext(ctx context.Context, x, y int) error {
	if ctx == nil {
		return fmt.Errorf("tap UI: nil context")
	}
	client, err := u.adbClient()
	if err != nil {
		return err
	}
	_, err = client.ShellContext(ctx, fmt.Sprintf("input tap %d %d", x, y))
	return err
}

// TypeText types text via `adb shell input text`.
func (u *Interactor) TypeText(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), u.operationTimeout())
	defer cancel()
	return u.TypeTextContext(ctx, text)
}

// TypeTextContext types text via `adb shell input text` while bounding the ADB
// command by ctx.
func (u *Interactor) TypeTextContext(ctx context.Context, text string) error {
	if ctx == nil {
		return fmt.Errorf("type UI text: nil context")
	}
	client, err := u.adbClient()
	if err != nil {
		return err
	}
	_, err = client.ShellContext(ctx, "input text "+shellQuote(text))
	return err
}

// shellQuote returns one POSIX-shell word. Android's shell accepts single
// quotes, allowing spaces and shell metacharacters to reach `input text` as a
// single literal argument.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func (u *Interactor) tapElementContext(t testing.TB, ctx context.Context, el *Element) {
	t.Helper()
	if el == nil {
		t.Fatal("tap element: nil element")
		return
	}
	cx, cy := el.Bounds.CenterX(), el.Bounds.CenterY()
	if err := u.TapContext(ctx, cx, cy); err != nil {
		t.Fatalf("tap(%d, %d): %v", cx, cy, err)
	}
}

func (u *Interactor) waitForElement(text string, timeout time.Duration) (Element, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return u.waitForElementContext(ctx, text, timeout)
}

func (u *Interactor) waitForElementContext(ctx context.Context, text string, timeout time.Duration) (Element, error) {
	var lastElements []Element
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return Element{}, waitError(err, lastErr, timeout, "text", text, lastElements)
		}
		elements, err := u.DumpContext(ctx)
		if err == nil {
			lastElements = elements
			u.autoDismissANRContext(ctx, elements)
			found := FindByText(elements, text)
			if len(found) > 0 {
				// Prefer clickable element
				for _, e := range found {
					if e.Clickable {
						return e, nil
					}
				}
				return found[0], nil
			}
		} else {
			lastErr = err
		}
		if err := waitForPoll(ctx); err != nil {
			return Element{}, waitError(err, lastErr, timeout, "text", text, lastElements)
		}
	}
}

func (u *Interactor) waitForElementByIDContext(ctx context.Context, resourceID string, timeout time.Duration) (Element, error) {
	var lastElements []Element
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return Element{}, waitError(err, lastErr, timeout, "resource-id", resourceID, lastElements)
		}
		elements, err := u.DumpContext(ctx)
		if err == nil {
			lastElements = elements
			u.autoDismissANRContext(ctx, elements)
			found := FindByResourceID(elements, resourceID)
			if len(found) > 0 {
				return found[0], nil
			}
		} else {
			lastErr = err
		}
		if err := waitForPoll(ctx); err != nil {
			return Element{}, waitError(err, lastErr, timeout, "resource-id", resourceID, lastElements)
		}
	}
}

// autoDismissANR taps "Wait" if a SystemUI ANR dialog is showing.
func (u *Interactor) autoDismissANRContext(ctx context.Context, elements []Element) {
	anr := FindByText(elements, systemUIANRText)
	if len(anr) == 0 {
		return
	}
	wait := FindByText(elements, "Wait")
	if len(wait) > 0 {
		_ = u.TapContext(ctx, wait[0].Bounds.CenterX(), wait[0].Bounds.CenterY())
	}
}

func (u *Interactor) resolveTimeout(timeout []time.Duration) time.Duration {
	if len(timeout) > 0 && timeout[0] > 0 {
		return timeout[0]
	}
	return u.operationTimeout()
}

func (u *Interactor) operationTimeout() time.Duration {
	if u != nil && u.Timeout > 0 {
		return u.Timeout
	}
	return defaultTimeout
}

func (u *Interactor) adbClient() (*adb.Client, error) {
	if u == nil {
		return nil, fmt.Errorf("UI interactor is nil")
	}
	if u.ADB == nil {
		return nil, fmt.Errorf("UI interactor ADB client is nil")
	}
	return u.ADB, nil
}

func waitForPoll(ctx context.Context) error {
	timer := time.NewTimer(pollInterval)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func waitError(
	ctxErr error,
	lastErr error,
	timeout time.Duration,
	kind string,
	value string,
	elements []Element,
) error {
	message := fmt.Sprintf("timeout after %v waiting for %s %q. Visible: %s", timeout, kind, value, visibleTexts(elements))
	if lastErr != nil {
		return fmt.Errorf("%s: %w (last dump error: %v)", message, ctxErr, lastErr)
	}
	return fmt.Errorf("%s: %w", message, ctxErr)
}

// visibleTexts returns a summary of visible text elements for error messages.
func visibleTexts(elements []Element) string {
	var texts []string
	for _, e := range elements {
		if e.Text != "" {
			texts = append(texts, fmt.Sprintf("%q", e.Text))
		}
	}
	if len(texts) == 0 {
		return "(none)"
	}
	return strings.Join(texts, ", ")
}
