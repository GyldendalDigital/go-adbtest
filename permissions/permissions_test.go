package permissions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/ui"
)

const testControllerPackage = "com.android.permissioncontroller"

type stubPermissionUI struct {
	dumpFn func() ([]ui.Element, error)
	tapFn  func(int, int) error
}

type blockingPermissionUI struct{}

func (blockingPermissionUI) DumpContext(ctx context.Context) ([]ui.Element, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (blockingPermissionUI) TapContext(context.Context, int, int) error {
	return nil
}

func (s *stubPermissionUI) DumpContext(context.Context) ([]ui.Element, error) {
	if s.dumpFn == nil {
		return nil, nil
	}
	return s.dumpFn()
}

func (s *stubPermissionUI) TapContext(_ context.Context, x, y int) error {
	if s.tapFn == nil {
		return nil
	}
	return s.tapFn(x, y)
}

type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1, 0)}
}

func (c *fakeClock) Now() time.Time {
	return c.now
}

func (c *fakeClock) Sleep(duration time.Duration) {
	c.sleeps = append(c.sleeps, duration)
	c.now = c.now.Add(duration)
}

func handlerWithUI(driver permissionUI) *Handler {
	return &Handler{
		uiOverride: driver,
		sleepFn:    func(time.Duration) {},
	}
}

func handlerWithClock(driver permissionUI, clock *fakeClock) *Handler {
	return &Handler{
		uiOverride: driver,
		nowFn:      clock.Now,
		sleepFn:    clock.Sleep,
	}
}

func element(packageName, text, resourceID string, clickable bool, x int) ui.Element {
	return ui.Element{
		Package:    packageName,
		Text:       text,
		ResourceID: resourceID,
		Clickable:  clickable,
		Bounds:     ui.Rect{X1: x, Y1: 100, X2: x + 20, Y2: 120},
	}
}

func TestResolveTimeout(t *testing.T) {
	tests := []struct {
		name    string
		timeout []time.Duration
		want    time.Duration
	}{
		{name: "omitted", want: defaultTimeout},
		{name: "zero", timeout: []time.Duration{0}, want: defaultTimeout},
		{name: "negative", timeout: []time.Duration{-time.Second}, want: defaultTimeout},
		{name: "custom", timeout: []time.Duration{5 * time.Second}, want: 5 * time.Second},
		{name: "first value wins", timeout: []time.Duration{2 * time.Second, 9 * time.Second}, want: 2 * time.Second},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveTimeout(test.timeout); got != test.want {
				t.Fatalf("resolveTimeout(%v) = %v, want %v", test.timeout, got, test.want)
			}
		})
	}
}

func TestIsPermissionDialogRequiresControllerOwnedAction(t *testing.T) {
	for _, packageName := range permissionControllerPackages {
		t.Run(packageName, func(t *testing.T) {
			elements := []ui.Element{element(packageName, "While using the app", "", true, 0)}
			if !isPermissionDialog(elements) {
				t.Fatal("controller-owned permission action was not recognized")
			}
		})
	}

	tests := []struct {
		name     string
		elements []ui.Element
		want     bool
	}{
		{
			name:     "app-owned Allow is not a permission dialog",
			elements: []ui.Element{element("com.example.app", "Allow", "", true, 0)},
		},
		{
			name:     "controller package alone is insufficient",
			elements: []ui.Element{element(testControllerPackage, "Permissions", "", true, 0)},
		},
		{
			name: "known resource ID supports localized text",
			elements: []ui.Element{
				element(testControllerPackage, "Autoriser", "com.android.permissioncontroller:id/permission_allow_button", true, 0),
			},
			want: true,
		},
		{
			name: "selected media resource ID is a permission dialog",
			elements: []ui.Element{
				element(testControllerPackage, "Fotos auswählen", "com.android.permissioncontroller:id/permission_allow_selected_button", true, 0),
			},
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isPermissionDialog(test.elements); got != test.want {
				t.Fatalf("isPermissionDialog() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestFindButtonSelection(t *testing.T) {
	t.Run("full media access is preferred", func(t *testing.T) {
		elements := []ui.Element{
			element(testControllerPackage, "While using the app", "", true, 10),
			element(testControllerPackage, "Tout autoriser", "com.android.permissioncontroller:id/permission_allow_all_button", true, 30),
		}
		got, _, ok := findButton(elements, grantButtonChoices)
		if !ok {
			t.Fatal("findButton() did not find a grant button")
		}
		if got.Bounds.CenterX() != 40 {
			t.Fatalf("selected x = %d, want full-access button x = 40", got.Bounds.CenterX())
		}
	})

	t.Run("grant preference beats hierarchy order", func(t *testing.T) {
		elements := []ui.Element{
			element(testControllerPackage, "Only this time", "", true, 10),
			element(testControllerPackage, "While using the app", "", true, 30),
		}
		got, _, ok := findButton(elements, grantButtonChoices)
		if !ok {
			t.Fatal("findButton() did not find a grant button")
		}
		if got.Bounds.CenterX() != 40 {
			t.Fatalf("selected x = %d, want preferred foreground button x = 40", got.Bounds.CenterX())
		}
	})

	t.Run("resource ID is preferred within an option", func(t *testing.T) {
		elements := []ui.Element{
			element(testControllerPackage, "While using the app", "", true, 10),
			element(testControllerPackage, "Lors de l'utilisation", "com.google.android.permissioncontroller:id/permission_allow_foreground_only_button", true, 30),
		}
		got, description, ok := findButton(elements, grantButtonChoices)
		if !ok {
			t.Fatal("findButton() did not find a grant button")
		}
		if got.Bounds.CenterX() != 40 || !strings.Contains(description, "resource-id") {
			t.Fatalf("selected (%d, %q), want resource-id button at x = 40", got.Bounds.CenterX(), description)
		}
	})

	t.Run("clickable match is preferred", func(t *testing.T) {
		elements := []ui.Element{
			element(testControllerPackage, "Allow", "", false, 10),
			element(testControllerPackage, "Allow", "", true, 30),
		}
		got, _, ok := findButton(elements, grantButtonChoices)
		if !ok {
			t.Fatal("findButton() did not find a grant button")
		}
		if got.Bounds.CenterX() != 40 {
			t.Fatalf("selected x = %d, want clickable button x = 40", got.Bounds.CenterX())
		}
	})

	t.Run("app-owned decoy is ignored", func(t *testing.T) {
		elements := []ui.Element{
			element("com.example.app", "Allow", "", true, 10),
			element(testControllerPackage, "Allow", "", true, 30),
		}
		got, _, ok := findButton(elements, grantButtonChoices)
		if !ok {
			t.Fatal("findButton() did not find a grant button")
		}
		if got.Package != testControllerPackage {
			t.Fatalf("selected package = %q, want %q", got.Package, testControllerPackage)
		}
	})

	t.Run("deny and do not ask again is lower priority", func(t *testing.T) {
		elements := []ui.Element{
			element(testControllerPackage, "Ne plus demander", "com.android.permissioncontroller:id/permission_deny_and_dont_ask_again_button", true, 10),
			element(testControllerPackage, "Deny", "", true, 30),
		}
		got, _, ok := findButton(elements, denyButtonChoices)
		if !ok {
			t.Fatal("findButton() did not find a deny button")
		}
		if got.Bounds.CenterX() != 40 {
			t.Fatalf("selected x = %d, want ordinary deny button x = 40", got.Bounds.CenterX())
		}
	})
}

func TestGrantTapsSupportedVariants(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		resourceID string
	}{
		{name: "API 30 foreground", text: "While using the app"},
		{name: "API 30 one time", text: "Only this time"},
		{name: "API 23 title case", text: "Allow"},
		{name: "API 23 upper case", text: "ALLOW"},
		{
			name:       "localized resource ID",
			text:       "Autoriser",
			resourceID: "com.android.permissioncontroller:id/permission_allow_button",
		},
		{
			name:       "Android 14 full photo access",
			text:       "Alle zulassen",
			resourceID: "com.android.permissioncontroller:id/permission_allow_all_button",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var taps [][2]int
			driver := &stubPermissionUI{
				dumpFn: func() ([]ui.Element, error) {
					return []ui.Element{element(testControllerPackage, test.text, test.resourceID, true, 20)}, nil
				},
				tapFn: func(x, y int) error {
					taps = append(taps, [2]int{x, y})
					return nil
				},
			}

			if err := handlerWithUI(driver).grant(time.Second); err != nil {
				t.Fatalf("grant() error: %v", err)
			}
			if len(taps) != 1 || taps[0] != [2]int{30, 110} {
				t.Fatalf("taps = %v, want [(30,110)]", taps)
			}
		})
	}
}

func TestGrantSelectedTapsLimitedMediaChoice(t *testing.T) {
	var taps [][2]int
	driver := &stubPermissionUI{
		dumpFn: func() ([]ui.Element, error) {
			return []ui.Element{
				element(testControllerPackage, "Fotos auswählen", "com.android.permissioncontroller:id/permission_allow_selected_button", true, 20),
			}, nil
		},
		tapFn: func(x, y int) error {
			taps = append(taps, [2]int{x, y})
			return nil
		},
	}

	if err := handlerWithUI(driver).grantSelected(time.Second); err != nil {
		t.Fatalf("grantSelected() error: %v", err)
	}
	if len(taps) != 1 || taps[0] != [2]int{30, 110} {
		t.Fatalf("taps = %v, want [(30,110)]", taps)
	}
}

func TestDenyTapsSupportedVariants(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		resourceID string
	}{
		{name: "API 30 straight apostrophe", text: "Don't allow"},
		{name: "API 30 typographic apostrophe", text: "Don’t allow"},
		{name: "API 23 title case", text: "Deny"},
		{name: "API 23 upper case", text: "DENY"},
		{
			name:       "localized resource ID",
			text:       "Refuser",
			resourceID: "com.android.permissioncontroller:id/permission_deny_button",
		},
		{
			name:       "deny and do not ask again resource ID",
			text:       "Ne plus demander",
			resourceID: "com.android.permissioncontroller:id/permission_deny_and_dont_ask_again_button",
		},
		{
			name:       "deny more selected media resource ID",
			text:       "Nicht mehr zulassen",
			resourceID: "com.android.permissioncontroller:id/permission_dont_allow_more_selected_button",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var taps [][2]int
			driver := &stubPermissionUI{
				dumpFn: func() ([]ui.Element, error) {
					return []ui.Element{element(testControllerPackage, test.text, test.resourceID, true, 40)}, nil
				},
				tapFn: func(x, y int) error {
					taps = append(taps, [2]int{x, y})
					return nil
				},
			}

			if err := handlerWithUI(driver).deny(time.Second); err != nil {
				t.Fatalf("deny() error: %v", err)
			}
			if len(taps) != 1 || taps[0] != [2]int{50, 110} {
				t.Fatalf("taps = %v, want [(50,110)]", taps)
			}
		})
	}
}

func TestGrantTimeoutIgnoresAppOwnedText(t *testing.T) {
	clock := newFakeClock()
	tapCalls := 0
	driver := &stubPermissionUI{
		dumpFn: func() ([]ui.Element, error) {
			return []ui.Element{element("com.example.app", "Allow", "", true, 0)}, nil
		},
		tapFn: func(int, int) error {
			tapCalls++
			return nil
		},
	}

	err := handlerWithClock(driver, clock).grant(time.Second)
	if err == nil || !strings.Contains(err.Error(), "no matching controller-owned button") {
		t.Fatalf("grant() error = %v, want controller-owned selection timeout", err)
	}
	if tapCalls != 0 {
		t.Fatalf("Tap called %d times for app-owned decoy, want 0", tapCalls)
	}
	if len(clock.sleeps) != 2 {
		t.Fatalf("retry sleeps = %v, want two deterministic sleeps", clock.sleeps)
	}
}

func TestGrantTimeoutRetainsDumpError(t *testing.T) {
	clock := newFakeClock()
	dumpCalls := 0
	driver := &stubPermissionUI{
		dumpFn: func() ([]ui.Element, error) {
			dumpCalls++
			return nil, errors.New("adb offline")
		},
	}

	err := handlerWithClock(driver, clock).grant(time.Second)
	if err == nil || !strings.Contains(err.Error(), "dump UI hierarchy: adb offline") {
		t.Fatalf("grant() error = %v, want retained dump error", err)
	}
	if dumpCalls != 3 {
		t.Fatalf("Dump called %d times, want 3", dumpCalls)
	}
}

func TestGrantTimeoutBoundsBlockedDump(t *testing.T) {
	started := time.Now()
	err := handlerWithUI(blockingPermissionUI{}).grant(50 * time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("grant() error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("grant() took %v with a 50ms timeout", elapsed)
	}
}

func TestGrantTimeoutRetainsTapError(t *testing.T) {
	clock := newFakeClock()
	tapCalls := 0
	driver := &stubPermissionUI{
		dumpFn: func() ([]ui.Element, error) {
			return []ui.Element{element(testControllerPackage, "Allow", "", true, 0)}, nil
		},
		tapFn: func(int, int) error {
			tapCalls++
			return errors.New("input tap failed")
		},
	}

	err := handlerWithClock(driver, clock).grant(time.Second)
	if err == nil || !strings.Contains(err.Error(), "input tap failed") {
		t.Fatalf("grant() error = %v, want retained tap error", err)
	}
	if tapCalls != 3 {
		t.Fatalf("Tap called %d times, want 3", tapCalls)
	}
}

func TestHandlerWithoutUIReturnsErrorsInsteadOfPanicking(t *testing.T) {
	handler := NewHandler(nil)
	if err := handler.grant(time.Second); err == nil || !strings.Contains(err.Error(), "interactor is nil") {
		t.Fatalf("grant() error = %v, want nil interactor error", err)
	}
	if handler.IsVisible() {
		t.Fatal("IsVisible() = true with nil UI")
	}

	var nilHandler *Handler
	if err := nilHandler.deny(time.Second); err == nil || !strings.Contains(err.Error(), "handler is nil") {
		t.Fatalf("deny() error = %v, want nil handler error", err)
	}

	interactor := &ui.Interactor{}
	handler = NewHandler(interactor)
	if handler.UI != interactor {
		t.Fatal("NewHandler() did not retain the UI interactor")
	}
	if err := handler.grant(time.Second); err == nil || !strings.Contains(err.Error(), "ADB client is nil") {
		t.Fatalf("grant() error = %v, want nil ADB client error", err)
	}
}

func TestIsVisibleHandlesDumpError(t *testing.T) {
	driver := &stubPermissionUI{
		dumpFn: func() ([]ui.Element, error) {
			return nil, errors.New("dump failed")
		},
	}
	handler := handlerWithUI(driver)

	if handler.IsVisible() {
		t.Fatal("IsVisible() = true after dump error")
	}
	if _, err := handler.permissionDialogVisible(); err == nil || !strings.Contains(err.Error(), "dump failed") {
		t.Fatalf("permissionDialogVisible() error = %v, want propagated dump error", err)
	}
}

func TestGrantAllDrainsSequentialDialogs(t *testing.T) {
	clock := newFakeClock()
	dialogs := [][]ui.Element{
		{element(testControllerPackage, "While using the app", "", true, 0)},
		{element(testControllerPackage, "Only this time", "", true, 20)},
		{element(testControllerPackage, "Allow", "", true, 40)},
		nil,
	}
	current := 0
	var taps [][2]int
	driver := &stubPermissionUI{
		dumpFn: func() ([]ui.Element, error) {
			return dialogs[current], nil
		},
		tapFn: func(x, y int) error {
			taps = append(taps, [2]int{x, y})
			current++
			return nil
		},
	}
	handler := handlerWithClock(driver, clock)

	if err := handler.grantAll(time.Second); err != nil {
		t.Fatalf("grantAll() error: %v", err)
	}
	wantTaps := [][2]int{{10, 110}, {30, 110}, {50, 110}}
	if len(taps) != len(wantTaps) {
		t.Fatalf("taps = %v, want %v", taps, wantTaps)
	}
	for i := range wantTaps {
		if taps[i] != wantTaps[i] {
			t.Fatalf("tap %d = %v, want %v", i, taps[i], wantTaps[i])
		}
	}
	if len(clock.sleeps) != 2 {
		t.Fatalf("transition polls = %v, want two polls through the remaining timeout", clock.sleeps)
	}
	for _, duration := range clock.sleeps {
		if duration != permissionPollInterval {
			t.Fatalf("transition poll = %v, want %v", duration, permissionPollInterval)
		}
	}
}

func TestGrantAllNoDialogIsNoop(t *testing.T) {
	clock := newFakeClock()
	dumpCalls := 0
	tapCalls := 0
	driver := &stubPermissionUI{
		dumpFn: func() ([]ui.Element, error) {
			dumpCalls++
			return nil, nil
		},
		tapFn: func(int, int) error {
			tapCalls++
			return nil
		},
	}

	if err := handlerWithClock(driver, clock).grantAll(time.Second); err != nil {
		t.Fatalf("grantAll() error: %v", err)
	}
	if tapCalls != 0 {
		t.Fatalf("Tap called %d times, want 0", tapCalls)
	}
	if dumpCalls != 3 {
		t.Fatalf("Dump called %d times, want initial poll through timeout (3 calls)", dumpCalls)
	}
	if len(clock.sleeps) != 2 {
		t.Fatalf("retry sleeps = %v, want two deterministic sleeps", clock.sleeps)
	}
}

func TestGrantAllWaitsForInitialDialog(t *testing.T) {
	clock := newFakeClock()
	dumpCalls := 0
	dialogVisible := false
	tapCalls := 0
	driver := &stubPermissionUI{
		dumpFn: func() ([]ui.Element, error) {
			dumpCalls++
			if dumpCalls == 2 {
				dialogVisible = true
			}
			if dialogVisible {
				return []ui.Element{element(testControllerPackage, "Allow", "", true, 0)}, nil
			}
			return nil, nil
		},
		tapFn: func(int, int) error {
			tapCalls++
			dialogVisible = false
			return nil
		},
	}

	if err := handlerWithClock(driver, clock).grantAll(2 * time.Second); err != nil {
		t.Fatalf("grantAll() error: %v", err)
	}
	if tapCalls != 1 {
		t.Fatalf("Tap called %d times, want 1", tapCalls)
	}
	if len(clock.sleeps) != 4 {
		t.Fatalf("sleeps = %v, want initial and bounded transition polling", clock.sleeps)
	}
	for _, duration := range clock.sleeps {
		if duration != permissionPollInterval {
			t.Fatalf("poll duration = %v, want %v", duration, permissionPollInterval)
		}
	}
}

func TestGrantAllRetriesTransientVisibilityError(t *testing.T) {
	clock := newFakeClock()
	dumpCalls := 0
	dialogVisible := true
	tapCalls := 0
	driver := &stubPermissionUI{
		dumpFn: func() ([]ui.Element, error) {
			dumpCalls++
			if dumpCalls == 1 {
				return nil, errors.New("temporary dump failure")
			}
			if dialogVisible {
				return []ui.Element{element(testControllerPackage, "Allow", "", true, 0)}, nil
			}
			return nil, nil
		},
		tapFn: func(int, int) error {
			tapCalls++
			dialogVisible = false
			return nil
		},
	}

	if err := handlerWithClock(driver, clock).grantAll(2 * time.Second); err != nil {
		t.Fatalf("grantAll() error: %v", err)
	}
	if tapCalls != 1 {
		t.Fatalf("Tap called %d times, want 1", tapCalls)
	}
	if len(clock.sleeps) != 4 {
		t.Fatalf("sleeps = %v, want retry and bounded transition polling", clock.sleeps)
	}
	for _, duration := range clock.sleeps {
		if duration != permissionPollInterval {
			t.Fatalf("poll duration = %v, want %v", duration, permissionPollInterval)
		}
	}
}

func TestGrantAllWaitsForDelayedNextDialog(t *testing.T) {
	clock := newFakeClock()
	granted := 0
	var taps [][2]int
	driver := &stubPermissionUI{
		dumpFn: func() ([]ui.Element, error) {
			switch {
			case granted == 0:
				return []ui.Element{element(testControllerPackage, "Allow", "", true, 0)}, nil
			case granted == 1 && clock.Now().Before(time.Unix(1, 0).Add(1500*time.Millisecond)):
				return nil, nil
			case granted == 1:
				return []ui.Element{element(testControllerPackage, "Only this time", "", true, 20)}, nil
			default:
				return nil, nil
			}
		},
		tapFn: func(x, y int) error {
			taps = append(taps, [2]int{x, y})
			granted++
			return nil
		},
	}

	if err := handlerWithClock(driver, clock).grantAll(5 * time.Second); err != nil {
		t.Fatalf("grantAll() error: %v", err)
	}
	if len(taps) != 2 {
		t.Fatalf("taps = %v, want both the immediate and delayed permission dialogs", taps)
	}
	if clock.Now().Before(time.Unix(1, 0).Add(1500 * time.Millisecond)) {
		t.Fatalf("grantAll returned before delayed dialog appeared at fake time %v", clock.Now())
	}
}

func TestGrantAllStopsAtFiveDialogs(t *testing.T) {
	dialogs := make([][]ui.Element, maxSequentialPermissionOps+1)
	for i := range dialogs {
		dialogs[i] = []ui.Element{element(testControllerPackage, "Allow", "", true, i*20)}
	}
	current := 0
	tapCalls := 0
	driver := &stubPermissionUI{
		dumpFn: func() ([]ui.Element, error) {
			return dialogs[current], nil
		},
		tapFn: func(int, int) error {
			tapCalls++
			current++
			return nil
		},
	}

	err := handlerWithUI(driver).grantAll(time.Second)
	if err == nil || !strings.Contains(err.Error(), "maximum of 5") {
		t.Fatalf("grantAll() error = %v, want five-dialog cap error", err)
	}
	if tapCalls != maxSequentialPermissionOps {
		t.Fatalf("Tap called %d times, want %d", tapCalls, maxSequentialPermissionOps)
	}
}
