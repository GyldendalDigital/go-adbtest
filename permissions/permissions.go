// Package permissions handles Android runtime permission dialogs,
// abstracting away API-level differences in button wording.
package permissions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/ui"
)

const (
	defaultTimeout             = 10 * time.Second
	permissionPollInterval     = 500 * time.Millisecond
	permissionTransitionWindow = 2 * time.Second
	maxSequentialPermissionOps = 5
)

// Button text and resource IDs vary by API level. Choices are ordered by the
// access level Grant should prefer when a dialog offers multiple options.
var (
	grantButtonChoices = []buttonChoice{
		{
			name:        "allow all",
			resourceIDs: []string{"permission_allow_all_button"},
			texts:       []string{"Allow all", "Allow all photos", "Allow all photos and videos", "Allow all videos"},
		},
		{
			name:        "allow while using the app",
			resourceIDs: []string{"permission_allow_foreground_only_button"},
			texts:       []string{"While using the app"},
		},
		{
			name:        "allow only this time",
			resourceIDs: []string{"permission_allow_one_time_button"},
			texts:       []string{"Only this time"},
		},
		{
			name:        "allow",
			resourceIDs: []string{"permission_allow_button"},
			texts:       []string{"Allow", "ALLOW"},
		},
	}
	selectedButtonChoices = []buttonChoice{
		{
			name:        "select limited media access",
			resourceIDs: []string{"permission_allow_selected_button"},
			texts:       []string{"Select photos", "Select videos", "Select photos and videos"},
		},
	}
	denyButtonChoices = []buttonChoice{
		{
			name:        "don't allow",
			resourceIDs: []string{"permission_deny_button"},
			texts:       []string{"Don't allow", "Don’t allow"},
		},
		{
			name:  "deny",
			texts: []string{"Deny", "DENY"},
		},
		{
			name:        "deny and don't ask again",
			resourceIDs: []string{"permission_deny_and_dont_ask_again_button"},
		},
		{
			name:        "don't allow more selected media",
			resourceIDs: []string{"permission_dont_allow_more_selected_button"},
			texts:       []string{"Don't allow more", "Don’t allow more"},
		},
	}

	permissionControllerPackages = []string{
		"com.google.android.permissioncontroller",
		"com.android.permissioncontroller",
		"com.google.android.packageinstaller",
		"com.android.packageinstaller",
	}
)

type buttonChoice struct {
	name        string
	resourceIDs []string
	texts       []string
}

type permissionUI interface {
	DumpContext(context.Context) ([]ui.Element, error)
	TapContext(context.Context, int, int) error
}

// Handler interacts with Android runtime permission dialogs. Helper methods use
// the first positive timeout override and otherwise wait up to ten seconds.
type Handler struct {
	// UI is the native-UI interactor used to inspect and tap permission dialogs.
	UI *ui.Interactor

	// These fields are deterministic test seams. Production handlers use UI,
	// time.Now, and time.Sleep.
	uiOverride permissionUI
	nowFn      func() time.Time
	sleepFn    func(time.Duration)
}

// NewHandler creates a Handler with the given UI interactor.
func NewHandler(interactor *ui.Interactor) *Handler {
	return &Handler{UI: interactor}
}

// Grant taps the most complete recognized allow option on the current
// permission dialog, preferring full access over foreground or one-time access.
func (h *Handler) Grant(t testing.TB, timeout ...time.Duration) {
	t.Helper()
	if err := h.grant(resolveTimeout(timeout)); err != nil {
		t.Fatalf("grant permission: %v", err)
	}
}

// GrantSelected taps Android's selected-media option. That option can open the
// system photo picker, which the caller must complete separately.
func (h *Handler) GrantSelected(t testing.TB, timeout ...time.Duration) {
	t.Helper()
	if err := h.grantSelected(resolveTimeout(timeout)); err != nil {
		t.Fatalf("grant selected-media permission: %v", err)
	}
}

// Deny taps a recognized full-denial option on the current permission dialog.
func (h *Handler) Deny(t testing.TB, timeout ...time.Duration) {
	t.Helper()
	if err := h.deny(resolveTimeout(timeout)); err != nil {
		t.Fatalf("deny permission: %v", err)
	}
}

// GrantAll grants sequential permission dialogs until none appear during a
// bounded transition window. At most five dialogs are granted to guard against
// an unexpected infinite sequence.
func (h *Handler) GrantAll(t testing.TB, timeout ...time.Duration) {
	t.Helper()
	if err := h.grantAll(resolveTimeout(timeout)); err != nil {
		t.Fatalf("grant all permissions: %v", err)
	}
}

// IsVisible reports whether a recognized permission dialog is currently
// showing. It returns false when the UI hierarchy cannot be inspected.
func (h *Handler) IsVisible() bool {
	visible, err := h.permissionDialogVisible()
	return err == nil && visible
}

func (h *Handler) grant(timeout time.Duration) error {
	return h.tapFirst(grantButtonChoices, timeout)
}

func (h *Handler) deny(timeout time.Duration) error {
	return h.tapFirst(denyButtonChoices, timeout)
}

func (h *Handler) grantSelected(timeout time.Duration) error {
	return h.tapFirst(selectedButtonChoices, timeout)
}

func (h *Handler) grantAll(timeout time.Duration) error {
	if _, err := h.interactionUI(); err != nil {
		return err
	}

	sequenceDeadline := h.nowTime().Add(timeout)
	visible, err := h.permissionDialogVisibleWithRetry(timeout, true)
	if err != nil {
		return fmt.Errorf("check for permission dialog 1: %w", err)
	}
	for granted := 0; visible && granted < maxSequentialPermissionOps; granted++ {
		remaining := sequenceDeadline.Sub(h.nowTime())
		if remaining < 0 {
			remaining = 0
		}
		if err := h.grant(remaining); err != nil {
			return fmt.Errorf("grant permission dialog %d: %w", granted+1, err)
		}

		transitionTimeout := sequenceDeadline.Sub(h.nowTime())
		if transitionTimeout < 0 {
			transitionTimeout = 0
		}
		if transitionTimeout > permissionTransitionWindow {
			transitionTimeout = permissionTransitionWindow
		}
		visible, err = h.permissionDialogVisibleWithRetry(transitionTimeout, true)
		if err != nil {
			return fmt.Errorf("check for permission dialog %d: %w", granted+2, err)
		}
	}

	if visible {
		return fmt.Errorf("maximum of %d sequential permission grants reached and another dialog is still visible", maxSequentialPermissionOps)
	}
	return nil
}

func (h *Handler) permissionDialogVisible() (bool, error) {
	driver, err := h.interactionUI()
	if err != nil {
		return false, err
	}

	deadline := h.nowTime().Add(defaultTimeout)
	elements, err := h.dumpUntil(driver, deadline)
	if err != nil {
		return false, fmt.Errorf("dump UI hierarchy: %w", err)
	}
	return isPermissionDialog(elements), nil
}

// permissionDialogVisibleWithRetry always retries hierarchy errors. When
// waitForAppearance is true it also polls successful, dialog-free dumps until
// the timeout, covering the race between requesting a permission and Android
// rendering its dialog.
func (h *Handler) permissionDialogVisibleWithRetry(timeout time.Duration, waitForAppearance bool) (bool, error) {
	driver, err := h.interactionUI()
	if err != nil {
		return false, err
	}

	deadline := h.nowTime().Add(timeout)
	for {
		elements, dumpErr := h.dumpUntil(driver, deadline)
		if dumpErr == nil {
			if isPermissionDialog(elements) {
				return true, nil
			}
			if !waitForAppearance {
				return false, nil
			}
			if !h.waitToRetry(deadline) {
				return false, nil
			}
			continue
		}

		if !h.waitToRetry(deadline) {
			return false, fmt.Errorf("permission dialog visibility timed out after %v: dump UI hierarchy: %w", timeout, dumpErr)
		}
	}
}

func isPermissionDialog(elements []ui.Element) bool {
	if _, _, ok := findButton(elements, grantButtonChoices); ok {
		return true
	}
	if _, _, ok := findButton(elements, selectedButtonChoices); ok {
		return true
	}
	_, _, ok := findButton(elements, denyButtonChoices)
	return ok
}

// tapFirst polls for the highest-priority matching controller-owned button and
// taps it. Dump and tap failures are retried until the timeout and retained in
// the returned error.
func (h *Handler) tapFirst(choices []buttonChoice, timeout time.Duration) error {
	driver, err := h.interactionUI()
	if err != nil {
		return err
	}

	deadline := h.nowTime().Add(timeout)
	var lastErr error
	for {
		elements, dumpErr := h.dumpUntil(driver, deadline)
		if dumpErr != nil {
			lastErr = fmt.Errorf("dump UI hierarchy: %w", dumpErr)
		} else if element, description, ok := findButton(elements, choices); ok {
			x, y := element.Bounds.CenterX(), element.Bounds.CenterY()
			if tapErr := h.tapUntil(driver, deadline, x, y); tapErr == nil {
				return nil
			} else {
				lastErr = fmt.Errorf("tap %s at (%d,%d): %w", description, x, y, tapErr)
			}
		} else {
			lastErr = errors.New("no matching controller-owned button found")
		}

		if !h.waitToRetry(deadline) {
			return fmt.Errorf("permission button selection timed out after %v (tried %s): %w", timeout, describeChoices(choices), lastErr)
		}
	}
}

func findButton(elements []ui.Element, choices []buttonChoice) (ui.Element, string, bool) {
	for _, choice := range choices {
		if element, resourceID, ok := findByResourceID(elements, choice.resourceIDs); ok {
			return element, fmt.Sprintf("%s button (resource-id %q)", choice.name, resourceID), true
		}
		if element, text, ok := findByExactText(elements, choice.texts); ok {
			return element, fmt.Sprintf("%s button (text %q)", choice.name, text), true
		}
	}
	return ui.Element{}, "", false
}

func findByResourceID(elements []ui.Element, resourceIDs []string) (ui.Element, string, bool) {
	for _, resourceID := range resourceIDs {
		if element, ok := firstMatchingElement(elements, func(element ui.Element) bool {
			return resourceIDName(element.ResourceID) == resourceID
		}); ok {
			return element, resourceID, true
		}
	}
	return ui.Element{}, "", false
}

func findByExactText(elements []ui.Element, texts []string) (ui.Element, string, bool) {
	for _, text := range texts {
		if element, ok := firstMatchingElement(elements, func(element ui.Element) bool {
			return strings.TrimSpace(element.Text) == text
		}); ok {
			return element, text, true
		}
	}
	return ui.Element{}, "", false
}

func firstMatchingElement(elements []ui.Element, matches func(ui.Element) bool) (ui.Element, bool) {
	var first ui.Element
	found := false
	for _, element := range elements {
		if !isPermissionControllerPackage(element.Package) || !matches(element) {
			continue
		}
		if element.Clickable {
			return element, true
		}
		if !found {
			first = element
			found = true
		}
	}
	return first, found
}

func isPermissionControllerPackage(packageName string) bool {
	for _, candidate := range permissionControllerPackages {
		if packageName == candidate {
			return true
		}
	}
	return false
}

func resourceIDName(resourceID string) string {
	if slash := strings.LastIndexByte(resourceID, '/'); slash >= 0 {
		return resourceID[slash+1:]
	}
	return resourceID
}

func describeChoices(choices []buttonChoice) string {
	var descriptions []string
	for _, choice := range choices {
		for _, resourceID := range choice.resourceIDs {
			descriptions = append(descriptions, "resource-id "+resourceID)
		}
		for _, text := range choice.texts {
			descriptions = append(descriptions, fmt.Sprintf("text %q", text))
		}
	}
	return strings.Join(descriptions, ", ")
}

func (h *Handler) interactionUI() (permissionUI, error) {
	if h == nil {
		return nil, errors.New("permission handler is nil")
	}
	if h.uiOverride != nil {
		return h.uiOverride, nil
	}
	if h.UI == nil {
		return nil, errors.New("permission UI interactor is nil")
	}
	if h.UI.ADB == nil {
		return nil, errors.New("permission UI interactor ADB client is nil")
	}
	return h.UI, nil
}

func (h *Handler) nowTime() time.Time {
	if h != nil && h.nowFn != nil {
		return h.nowFn()
	}
	return time.Now()
}

func (h *Handler) sleepFor(duration time.Duration) {
	if h != nil && h.sleepFn != nil {
		h.sleepFn(duration)
		return
	}
	time.Sleep(duration)
}

func (h *Handler) waitToRetry(deadline time.Time) bool {
	now := h.nowTime()
	if !now.Before(deadline) {
		return false
	}

	delay := permissionPollInterval
	if remaining := deadline.Sub(now); remaining < delay {
		delay = remaining
	}
	h.sleepFor(delay)
	return true
}

func (h *Handler) dumpUntil(driver permissionUI, deadline time.Time) ([]ui.Element, error) {
	ctx, cancel := h.contextUntil(deadline)
	defer cancel()
	return driver.DumpContext(ctx)
}

func (h *Handler) tapUntil(driver permissionUI, deadline time.Time, x, y int) error {
	ctx, cancel := h.contextUntil(deadline)
	defer cancel()
	return driver.TapContext(ctx, x, y)
}

func (h *Handler) contextUntil(deadline time.Time) (context.Context, context.CancelFunc) {
	remaining := deadline.Sub(h.nowTime())
	if remaining <= 0 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx, cancel
	}
	return context.WithTimeout(context.Background(), remaining)
}

func resolveTimeout(timeout []time.Duration) time.Duration {
	if len(timeout) > 0 && timeout[0] > 0 {
		return timeout[0]
	}
	return defaultTimeout
}
