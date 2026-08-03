// Package androidsdk contains shared Android SDK discovery and process helpers.
package androidsdk

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const maxCommandOutput = 1 << 20

// CommandResult contains the bounded standard streams from an SDK command.
type CommandResult struct {
	Stdout string
	Stderr string
}

// Run executes an SDK tool directly without a host shell. Output retained for
// errors is capped, while progress receives the complete streams when set.
func Run(
	ctx context.Context,
	executable string,
	args []string,
	stdin io.Reader,
	progress io.Writer,
) (CommandResult, error) {
	// executable is resolved from the Android SDK or PATH and args are passed
	// directly rather than interpreted by a shell.
	//nolint:gosec // Running the caller-selected Android SDK tool is intentional.
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Stdin = stdin

	var stdout, stderr cappedBuffer
	if progress == nil {
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	} else {
		lockedProgress := &synchronizedWriter{writer: progress}
		cmd.Stdout = io.MultiWriter(&stdout, lockedProgress)
		cmd.Stderr = io.MultiWriter(&stderr, lockedProgress)
	}

	err := cmd.Run()
	result := CommandResult{
		Stdout: strings.TrimSpace(stdout.String()),
		Stderr: strings.TrimSpace(stderr.String()),
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	return result, err
}

// ResolveSDKRoot selects one coherent Android SDK root. ANDROID_HOME is the
// canonical variable; the deprecated ANDROID_SDK_ROOT remains supported when
// it is the only configured root.
func ResolveSDKRoot(explicit string) (string, error) {
	if root := strings.TrimSpace(explicit); root != "" {
		return validateSDKRoot(root)
	}

	androidHome := strings.TrimSpace(os.Getenv("ANDROID_HOME"))
	sdkRoot := strings.TrimSpace(os.Getenv("ANDROID_SDK_ROOT"))
	if androidHome != "" && sdkRoot != "" && !samePath(androidHome, sdkRoot) {
		return "", fmt.Errorf(
			"ANDROID_HOME %q and ANDROID_SDK_ROOT %q refer to different SDKs",
			androidHome,
			sdkRoot,
		)
	}
	if androidHome != "" {
		return validateSDKRoot(androidHome)
	}
	if sdkRoot != "" {
		return validateSDKRoot(sdkRoot)
	}

	for _, tool := range []string{"adb", "emulator", "avdmanager", "sdkmanager"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		root, ok := inferSDKRoot(path, tool)
		if !ok {
			continue
		}
		if validated, err := validateSDKRoot(root); err == nil {
			return validated, nil
		}
	}

	return "", fmt.Errorf("android SDK root not found: set ANDROID_HOME or add Android SDK tools to PATH")
}

// FindTool resolves a supported Android SDK tool from root. When root is
// empty, it resolves the tool from PATH instead.
func FindTool(root, name string) (string, error) {
	root = strings.TrimSpace(root)
	for _, candidate := range toolCandidates(root, name) {
		if isFile(candidate) {
			return candidate, nil
		}
	}
	if root != "" {
		return "", fmt.Errorf("%s not found in Android SDK %q", name, root)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH", name)
	}
	return path, nil
}

// AVDHomes returns Android's AVD search locations in precedence order.
func AVDHomes() ([]string, error) {
	var homes []string
	appendHome := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		path = filepath.Clean(path)
		for _, existing := range homes {
			if samePath(existing, path) {
				return
			}
		}
		homes = append(homes, path)
	}

	explicitAVDHome := strings.TrimSpace(os.Getenv("ANDROID_AVD_HOME"))
	userHome := strings.TrimSpace(os.Getenv("ANDROID_USER_HOME"))
	emulatorHome := strings.TrimSpace(os.Getenv("ANDROID_EMULATOR_HOME"))
	legacySDKHome := strings.TrimSpace(os.Getenv("ANDROID_SDK_HOME"))
	if explicitAVDHome == "" && (userHome != "" || emulatorHome != "" || legacySDKHome != "") {
		return nil, fmt.Errorf(
			"an Android home relocation variable is set; set ANDROID_AVD_HOME explicitly so avdmanager and emulator use the same AVD directory",
		)
	}

	appendHome(explicitAVDHome)
	if explicitAVDHome != "" {
		return homes, nil
	}
	if emulatorHome != "" {
		appendHome(filepath.Join(emulatorHome, "avd"))
	}
	if userHome != "" {
		appendHome(filepath.Join(userHome, "avd"))
	}
	home, err := os.UserHomeDir()
	if err != nil && len(homes) == 0 {
		return nil, fmt.Errorf("resolve Android AVD home: %w", err)
	}
	if err == nil {
		appendHome(filepath.Join(home, ".android", "avd"))
	}
	return homes, nil
}

func validateSDKRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve Android SDK root %q: %w", root, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", fmt.Errorf("android SDK root %q: %w", absolute, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("android SDK root %q is not a directory", absolute)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve Android SDK root symlinks %q: %w", absolute, err)
	}
	return resolved, nil
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr == nil {
		left = leftAbs
	}
	if rightErr == nil {
		right = rightAbs
	}
	if resolved, err := filepath.EvalSymlinks(left); err == nil {
		left = resolved
	}
	if resolved, err := filepath.EvalSymlinks(right); err == nil {
		right = resolved
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func inferSDKRoot(path, tool string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = resolved
	}
	directory := filepath.Dir(path)
	switch tool {
	case "adb":
		if filepath.Base(directory) == "platform-tools" {
			return filepath.Dir(directory), true
		}
	case "emulator":
		if filepath.Base(directory) == "emulator" {
			return filepath.Dir(directory), true
		}
	case "avdmanager", "sdkmanager":
		for current := directory; current != filepath.Dir(current); current = filepath.Dir(current) {
			if filepath.Base(current) == "cmdline-tools" {
				return filepath.Dir(current), true
			}
			if filepath.Base(current) == "tools" {
				return filepath.Dir(current), true
			}
		}
	}
	return "", false
}

func toolCandidates(root, name string) []string {
	if root == "" {
		return nil
	}
	var directories []string
	switch name {
	case "adb":
		directories = []string{filepath.Join(root, "platform-tools")}
	case "emulator":
		directories = []string{filepath.Join(root, "emulator")}
	case "avdmanager", "sdkmanager", "android":
		directories = commandLineToolDirectories(root)
	case "aapt", "aapt2":
		directories = buildToolDirectories(root)
	}

	var candidates []string
	for _, directory := range directories {
		for _, filename := range executableNames(name) {
			candidates = append(candidates, filepath.Join(directory, filename))
		}
	}
	return candidates
}

func commandLineToolDirectories(root string) []string {
	base := filepath.Join(root, "cmdline-tools")
	directories := []string{filepath.Join(base, "latest", "bin")}
	entries, err := os.ReadDir(base)
	if err == nil {
		versions := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() && entry.Name() != "latest" {
				versions = append(versions, entry.Name())
			}
		}
		sort.Slice(versions, func(i, j int) bool {
			return compareVersions(versions[i], versions[j]) > 0
		})
		for _, version := range versions {
			directories = append(directories, filepath.Join(base, version, "bin"))
		}
	}
	return append(directories, filepath.Join(root, "tools", "bin"))
}

func buildToolDirectories(root string) []string {
	base := filepath.Join(root, "build-tools")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			versions = append(versions, entry.Name())
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		return compareVersions(versions[i], versions[j]) > 0
	})
	directories := make([]string, 0, len(versions))
	for _, version := range versions {
		directories = append(directories, filepath.Join(base, version))
	}
	return directories
}

func executableNames(name string) []string {
	if runtime.GOOS != "windows" {
		return []string{name}
	}
	return []string{name + ".exe", name + ".bat", name + ".cmd", name}
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0
}

func compareVersions(left, right string) int {
	leftParts := strings.FieldsFunc(left, func(r rune) bool { return r < '0' || r > '9' })
	rightParts := strings.FieldsFunc(right, func(r rune) bool { return r < '0' || r > '9' })
	length := len(leftParts)
	if len(rightParts) > length {
		length = len(rightParts)
	}
	for index := 0; index < length; index++ {
		leftNumber, rightNumber := 0, 0
		if index < len(leftParts) {
			leftNumber, _ = strconv.Atoi(leftParts[index])
		}
		if index < len(rightParts) {
			rightNumber, _ = strconv.Atoi(rightParts[index])
		}
		if leftNumber < rightNumber {
			return -1
		}
		if leftNumber > rightNumber {
			return 1
		}
	}
	return strings.Compare(left, right)
}

type cappedBuffer struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	if len(data) >= maxCommandOutput {
		b.buffer.Reset()
		_, _ = b.buffer.Write(data[len(data)-maxCommandOutput:])
		b.truncated = true
		return written, nil
	}
	if overflow := b.buffer.Len() + len(data) - maxCommandOutput; overflow > 0 {
		_ = b.buffer.Next(overflow)
		b.truncated = true
	}
	_, _ = b.buffer.Write(data)
	return written, nil
}

func (b *cappedBuffer) String() string {
	if !b.truncated {
		return b.buffer.String()
	}
	return "... output truncated; showing tail ...\n" + b.buffer.String()
}

type synchronizedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *synchronizedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.writer.Write(data)
	return len(data), nil
}
