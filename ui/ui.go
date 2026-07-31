package ui

import (
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

// TapOnText finds an element by text and taps its center. Polls with retry.
func (u *Interactor) TapOnText(t testing.TB, text string, timeout ...time.Duration) {
	t.Helper()
	to := u.resolveTimeout(timeout)
	el, err := u.waitForElement(text, to)
	if err != nil {
		t.Fatalf("TapOnText(%q): %v", text, err)
	}
	u.tapElement(t, el)
}

// LongPressOnText finds an element by text and long-presses (swipe-in-place 1.5s).
func (u *Interactor) LongPressOnText(t testing.TB, text string, timeout ...time.Duration) {
	t.Helper()
	to := u.resolveTimeout(timeout)
	el, err := u.waitForElement(text, to)
	if err != nil {
		t.Fatalf("LongPressOnText(%q): %v", text, err)
	}
	cx, cy := el.Bounds.CenterX(), el.Bounds.CenterY()
	_, err = u.ADB.Shell(fmt.Sprintf("input swipe %d %d %d %d 1500", cx, cy, cx, cy))
	if err != nil {
		t.Fatalf("LongPressOnText(%q): input swipe: %v", text, err)
	}
}

// TapOnID finds an element by resource-id and taps its center.
func (u *Interactor) TapOnID(t testing.TB, resourceID string, timeout ...time.Duration) {
	t.Helper()
	to := u.resolveTimeout(timeout)
	el, err := u.waitForElementByID(resourceID, to)
	if err != nil {
		t.Fatalf("TapOnID(%q): %v", resourceID, err)
	}
	u.tapElement(t, el)
}

// WaitForText polls until an element with the given text appears.
func (u *Interactor) WaitForText(t testing.TB, text string, timeout ...time.Duration) {
	t.Helper()
	to := u.resolveTimeout(timeout)
	if _, err := u.waitForElement(text, to); err != nil {
		t.Fatalf("WaitForText(%q): %v", text, err)
	}
}

// AssertVisible asserts that text is currently visible in the UI hierarchy.
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

// AssertGone asserts that text is NOT visible in the UI hierarchy.
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
	// Try fast path: dump to stdout via /dev/tty
	out, err := u.ADB.Shell("uiautomator dump /dev/tty")
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

	// Fallback: dump to a device file, pull it locally, and parse it there.
	const remotePath = "/sdcard/ui.xml"
	if _, err := u.ADB.Shell("uiautomator dump /sdcard/ui.xml"); err != nil {
		return nil, fmt.Errorf("uiautomator fallback dump: %w (fast path: %v)", err, fastPathErr)
	}
	defer func() {
		_, _ = u.ADB.Shell("rm -f " + remotePath)
	}()

	tmpDir, err := os.MkdirTemp("", "go-adbtest-ui-")
	if err != nil {
		return nil, fmt.Errorf("create temporary directory for ui dump: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	localPath := filepath.Join(tmpDir, "ui.xml")
	if err := u.ADB.Pull(remotePath, localPath); err != nil {
		return nil, fmt.Errorf("pull ui dump: %w", err)
	}
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
	_, err := u.ADB.Shell(fmt.Sprintf("input tap %d %d", x, y))
	return err
}

// TypeText types text via `adb shell input text`.
func (u *Interactor) TypeText(text string) error {
	_, err := u.ADB.Shell("input text " + shellQuote(text))
	return err
}

// shellQuote returns one POSIX-shell word. Android's shell accepts single
// quotes, allowing spaces and shell metacharacters to reach `input text` as a
// single literal argument.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func (u *Interactor) tapElement(t testing.TB, el Element) {
	t.Helper()
	cx, cy := el.Bounds.CenterX(), el.Bounds.CenterY()
	if err := u.Tap(cx, cy); err != nil {
		t.Fatalf("tap(%d, %d): %v", cx, cy, err)
	}
}

func (u *Interactor) waitForElement(text string, timeout time.Duration) (Element, error) {
	deadline := time.Now().Add(timeout)
	var lastElements []Element
	for time.Now().Before(deadline) {
		elements, err := u.Dump()
		if err == nil {
			lastElements = elements
			u.autoDismissANR(elements)
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
		}
		time.Sleep(pollInterval)
	}
	return Element{}, fmt.Errorf("timeout after %v waiting for text %q. Visible: %s", timeout, text, visibleTexts(lastElements))
}

func (u *Interactor) waitForElementByID(resourceID string, timeout time.Duration) (Element, error) {
	deadline := time.Now().Add(timeout)
	var lastElements []Element
	for time.Now().Before(deadline) {
		elements, err := u.Dump()
		if err == nil {
			lastElements = elements
			u.autoDismissANR(elements)
			found := FindByResourceID(elements, resourceID)
			if len(found) > 0 {
				return found[0], nil
			}
		}
		time.Sleep(pollInterval)
	}
	return Element{}, fmt.Errorf("timeout after %v waiting for resource-id %q. Visible: %s", timeout, resourceID, visibleTexts(lastElements))
}

// autoDismissANR taps "Wait" if a SystemUI ANR dialog is showing.
func (u *Interactor) autoDismissANR(elements []Element) {
	anr := FindByText(elements, systemUIANRText)
	if len(anr) == 0 {
		return
	}
	wait := FindByText(elements, "Wait")
	if len(wait) > 0 {
		_ = u.Tap(wait[0].Bounds.CenterX(), wait[0].Bounds.CenterY())
	}
}

func (u *Interactor) resolveTimeout(timeout []time.Duration) time.Duration {
	if len(timeout) > 0 && timeout[0] > 0 {
		return timeout[0]
	}
	return u.Timeout
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
