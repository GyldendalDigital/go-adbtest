package ui

import (
	"fmt"
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
	ADB     *adb.Client
	Timeout time.Duration // default wait timeout
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
	if err == nil && strings.Contains(out, "<hierarchy") {
		// Strip any prefix before the XML
		idx := strings.Index(out, "<?xml")
		if idx < 0 {
			idx = strings.Index(out, "<hierarchy")
		}
		if idx >= 0 {
			return ParseDump([]byte(out[idx:]))
		}
	}

	// Fallback: dump to file, pull, parse
	if _, err := u.ADB.Shell("uiautomator dump /sdcard/ui.xml"); err != nil {
		return nil, fmt.Errorf("uiautomator dump: %w", err)
	}
	out, err = u.ADB.Shell("cat /sdcard/ui.xml")
	if err != nil {
		return nil, fmt.Errorf("cat ui dump: %w", err)
	}
	_, _ = u.ADB.Shell("rm /sdcard/ui.xml")

	return ParseDump([]byte(out))
}

// Tap sends an input tap at absolute coordinates.
func (u *Interactor) Tap(x, y int) error {
	_, err := u.ADB.Shell(fmt.Sprintf("input tap %d %d", x, y))
	return err
}

// TypeText types text via `adb shell input text`.
func (u *Interactor) TypeText(text string) error {
	// Escape spaces for adb shell input
	escaped := strings.ReplaceAll(text, " ", "%s")
	_, err := u.ADB.Shell("input text " + escaped)
	return err
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
