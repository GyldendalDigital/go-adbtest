package adbtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/GyldendalDigital/go-adbtest/internal/androidsdk"
)

const (
	defaultAVDDevice = "small_phone"
	defaultAVDTarget = "google_apis"
)

var ensureAVDLockRegistry = struct {
	sync.Mutex
	locks map[string]*ensureAVDLock
}{locks: make(map[string]*ensureAVDLock)}

type ensureAVDLock struct {
	token      chan struct{}
	references int
}

// AVDProfile describes an Android Virtual Device that EnsureAVD should
// validate or create. Name and APILevel are required. Device defaults to
// small_phone, Target defaults to google_apis, and Arch defaults to the
// accelerated architecture matching the host. InstallSystemImage must be set
// explicitly before EnsureAVD may download a missing system image.
type AVDProfile struct {
	// Name is the stable command-line AVD ID, for example go_adbtest_api_35.
	Name string
	// APILevel selects the Android system image API level.
	APILevel int
	// Device is an avdmanager hardware-profile ID. Empty uses small_phone.
	Device string
	// Target is a non-Play, non-ATD system-image target. Empty uses google_apis.
	Target string
	// Arch is the system-image ABI. Empty selects the ABI matching the current
	// Go process. On macOS, use a native Go toolchain rather than Rosetta.
	Arch string
	// InstallSystemImage permits sdkmanager to download a missing image. It
	// never accepts SDK licences; developers must do that explicitly.
	InstallSystemImage bool
	// Progress receives sdkmanager and avdmanager output when non-nil.
	Progress io.Writer
}

// AVD describes an AVD validated or created by EnsureAVD.
type AVD struct {
	// Name is the command-line AVD ID accepted by HeadlessAVD and the emulator.
	Name string
	// SystemImage is the sdkmanager package ID backing the AVD.
	SystemImage string
	// Created reports whether this EnsureAVD call created the AVD.
	Created bool
}

// HeadlessConfig returns the conservative accelerated launch configuration for
// this AVD and apk. It delegates to HeadlessAVD so safe launch flags remain in
// one place.
func (a AVD) HeadlessConfig(apk string) Config {
	return HeadlessAVD(a.Name, apk)
}

// EnsureAVD validates and reuses a matching named AVD or creates it from the
// requested profile. It never starts an emulator, overwrites an existing AVD,
// accepts SDK licences, or downloads an image unless InstallSystemImage is
// explicitly true. This provisioning path supports Linux x86_64 and native
// macOS amd64/arm64; Setup can still use separately managed AVDs elsewhere.
//
//nolint:gocritic // AVDProfile is a public value-style options struct by design.
func EnsureAVD(ctx context.Context, profile AVDProfile) (AVD, error) {
	return ensureAVDWithDependencies(ctx, profile, productionAVDDependencies())
}

type avdDependencies struct {
	hostArch       string
	hostOS         string
	getenv         func(string) string
	resolveSDKRoot func(string) (string, error)
	findTool       func(string, string) (string, error)
	avdHomes       func() ([]string, error)
	run            func(context.Context, string, []string, io.Reader, io.Writer) (androidsdk.CommandResult, error)
}

func productionAVDDependencies() avdDependencies {
	return avdDependencies{
		hostArch:       runtime.GOARCH,
		hostOS:         runtime.GOOS,
		getenv:         os.Getenv,
		resolveSDKRoot: androidsdk.ResolveSDKRoot,
		findTool:       androidsdk.FindTool,
		avdHomes:       androidsdk.AVDHomes,
		run:            androidsdk.Run,
	}
}

//nolint:gocritic // The private seam mirrors EnsureAVD's value-style API.
func ensureAVDWithDependencies(ctx context.Context, profile AVDProfile, deps avdDependencies) (AVD, error) {
	if ctx == nil {
		return AVD{}, fmt.Errorf("ensure AVD: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return AVD{}, fmt.Errorf("ensure AVD: %w", err)
	}
	if err := validateAVDDependencies(deps); err != nil {
		return AVD{}, err
	}

	normalized, err := normalizeAVDProfile(profile, deps.hostOS, deps.hostArch)
	if err != nil {
		return AVD{}, err
	}
	if strings.TrimSpace(deps.getenv("ANDROID_HOME")) == "" &&
		strings.TrimSpace(deps.getenv("ANDROID_SDK_ROOT")) == "" {
		return AVD{}, fmt.Errorf(
			"ensure AVD %q: set ANDROID_HOME (or legacy ANDROID_SDK_ROOT) so provisioning and Setup use the same Android SDK",
			normalized.Name,
		)
	}
	root, err := deps.resolveSDKRoot("")
	if err != nil {
		return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
	}
	release, err := acquireEnsureAVDLock(ctx, root)
	if err != nil {
		return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
	}
	defer release()
	imagePackage := avdSystemImagePackage(&normalized)
	result := AVD{Name: normalized.Name, SystemImage: imagePackage}
	emulatorPath, err := deps.findTool(root, "emulator")
	if err != nil {
		return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
	}
	listedAVDs, err := listAVDs(ctx, emulatorPath, deps.run)
	if err != nil {
		return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
	}

	homes, err := deps.avdHomes()
	if err != nil {
		return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
	}
	existing, found, err := androidsdk.AVDConfig(normalized.Name, homes)
	if err != nil {
		return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
	}
	listed := containsExactString(listedAVDs, normalized.Name)
	if listed != found {
		if listed {
			return AVD{}, fmt.Errorf(
				"ensure AVD %q: emulator lists the AVD but its registration metadata could not be verified",
				normalized.Name,
			)
		}
		return AVD{}, fmt.Errorf(
			"ensure AVD %q: registration metadata exists but emulator -list-avds does not list it; repair or remove it manually",
			normalized.Name,
		)
	}
	if found {
		if err := verifyAVDConfig(existing.Values, root, &normalized); err != nil {
			return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
		}
		if err := ensureSystemImage(ctx, root, &normalized, imagePackage, deps); err != nil {
			return AVD{}, err
		}
		return result, nil
	}

	imageInstalled := systemImageInstalled(root, &normalized)
	if !imageInstalled && !normalized.InstallSystemImage {
		if err := ensureSystemImage(ctx, root, &normalized, imagePackage, deps); err != nil {
			return AVD{}, err
		}
	}
	avdmanager, err := deps.findTool(root, "avdmanager")
	if err != nil {
		return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
	}
	profileAvailable, err := hardwareProfileAvailable(ctx, avdmanager, &normalized, deps.run)
	if err != nil {
		return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
	}
	if !imageInstalled {
		if err := ensureSystemImage(ctx, root, &normalized, imagePackage, deps); err != nil {
			return AVD{}, err
		}
		if !profileAvailable {
			profileAvailable, err = hardwareProfileAvailable(ctx, avdmanager, &normalized, deps.run)
			if err != nil {
				return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
			}
		}
	}
	if !profileAvailable {
		return AVD{}, fmt.Errorf(
			"ensure AVD %q: hardware profile %q is unavailable; inspect valid IDs with %s",
			normalized.Name,
			normalized.Device,
			formatCommand(avdmanager, []string{"list", "device", "-c"}),
		)
	}

	args := []string{
		"create", "avd",
		"--name", normalized.Name,
		"--package", imagePackage,
		"--device", normalized.Device,
	}
	commandResult, err := deps.run(
		ctx,
		avdmanager,
		args,
		strings.NewReader("no\n"),
		normalized.Progress,
	)
	if err != nil {
		createErr := commandFailure("create AVD", avdmanager, args, commandResult, err)
		matched, rediscoverErr := registeredAVDMatches(
			ctx,
			normalized.Name,
			homes,
			emulatorPath,
			root,
			&normalized,
			deps.run,
		)
		if rediscoverErr == nil && matched {
			return result, nil
		}
		if rediscoverErr != nil {
			return AVD{}, errors.Join(createErr, fmt.Errorf("rediscover AVD after create failure: %w", rediscoverErr))
		}
		return AVD{}, createErr
	}
	matched, err := registeredAVDMatches(
		ctx,
		normalized.Name,
		homes,
		emulatorPath,
		root,
		&normalized,
		deps.run,
	)
	if err != nil {
		return AVD{}, fmt.Errorf("verify created AVD %q: %w", normalized.Name, err)
	}
	if !matched {
		return AVD{}, fmt.Errorf(
			"verify created AVD %q: avdmanager succeeded but emulator -list-avds does not list it",
			normalized.Name,
		)
	}
	result.Created = true
	return result, nil
}

func acquireEnsureAVDLock(ctx context.Context, key string) (func(), error) {
	ensureAVDLockRegistry.Lock()
	lock := ensureAVDLockRegistry.locks[key]
	if lock == nil {
		lock = &ensureAVDLock{token: make(chan struct{}, 1)}
		ensureAVDLockRegistry.locks[key] = lock
	}
	lock.references++
	ensureAVDLockRegistry.Unlock()

	select {
	case lock.token <- struct{}{}:
		return func() {
			<-lock.token
			releaseEnsureAVDLockReference(key, lock)
		}, nil
	case <-ctx.Done():
		releaseEnsureAVDLockReference(key, lock)
		return nil, ctx.Err()
	}
}

func releaseEnsureAVDLockReference(key string, lock *ensureAVDLock) {
	ensureAVDLockRegistry.Lock()
	defer ensureAVDLockRegistry.Unlock()
	lock.references--
	if lock.references == 0 && ensureAVDLockRegistry.locks[key] == lock {
		delete(ensureAVDLockRegistry.locks, key)
	}
}

func validateAVDDependencies(deps avdDependencies) error {
	if strings.TrimSpace(deps.hostArch) == "" || strings.TrimSpace(deps.hostOS) == "" || deps.getenv == nil ||
		deps.resolveSDKRoot == nil || deps.findTool == nil ||
		deps.avdHomes == nil || deps.run == nil {
		return fmt.Errorf("ensure AVD: internal dependencies are incomplete")
	}
	return nil
}

//nolint:gocritic // Normalization returns an independent options value.
func normalizeAVDProfile(profile AVDProfile, hostOS, hostArch string) (AVDProfile, error) {
	profile.Name = strings.TrimSpace(profile.Name)
	profile.Device = strings.TrimSpace(profile.Device)
	profile.Target = strings.TrimSpace(profile.Target)
	profile.Arch = strings.TrimSpace(profile.Arch)

	if !validAVDName(profile.Name) {
		return AVDProfile{}, fmt.Errorf(
			"ensure AVD: name %q must start with a letter or digit and contain only letters, digits, dots, dashes, or underscores",
			profile.Name,
		)
	}
	if profile.APILevel < 1 || profile.APILevel > 999 {
		return AVDProfile{}, fmt.Errorf("ensure AVD %q: API level must be between 1 and 999", profile.Name)
	}
	if profile.Device == "" {
		profile.Device = defaultAVDDevice
	}
	if !validDeviceID(profile.Device) {
		return AVDProfile{}, fmt.Errorf("ensure AVD %q: invalid hardware profile %q", profile.Name, profile.Device)
	}
	if profile.Target == "" {
		profile.Target = defaultAVDTarget
	}
	if !validTarget(profile.Target) {
		return AVDProfile{}, fmt.Errorf("ensure AVD %q: invalid system-image target %q", profile.Name, profile.Target)
	}
	lowerTarget := strings.ToLower(profile.Target)
	if strings.Contains(lowerTarget, "playstore") || strings.Contains(lowerTarget, "_atd") || lowerTarget == "atd" {
		return AVDProfile{}, fmt.Errorf(
			"ensure AVD %q: target %q is not supported by the lightweight path; use a non-Play, non-ATD image",
			profile.Name,
			profile.Target,
		)
	}

	if hostOS != "linux" && hostOS != "darwin" {
		return AVDProfile{}, fmt.Errorf(
			"ensure AVD %q: host %q is not supported; lightweight provisioning requires Linux or macOS",
			profile.Name,
			hostOS,
		)
	}
	nativeArch, err := androidsdk.NativeArch(hostArch)
	if err != nil {
		return AVDProfile{}, fmt.Errorf("ensure AVD %q: %w", profile.Name, err)
	}
	if hostArch == "arm64" && hostOS != "darwin" {
		return AVDProfile{}, fmt.Errorf(
			"ensure AVD %q: the Android Emulator does not support accelerated %s/arm64 hosts",
			profile.Name,
			hostOS,
		)
	}
	if profile.Arch == "" {
		profile.Arch = nativeArch
	}
	if profile.Arch != nativeArch {
		return AVDProfile{}, fmt.Errorf(
			"ensure AVD %q: system-image architecture %q does not match accelerated host architecture %q",
			profile.Name,
			profile.Arch,
			nativeArch,
		)
	}
	return profile, nil
}

func validAVDName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for index, character := range []byte(name) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' {
			continue
		}
		if index > 0 && (character == '.' || character == '-' || character == '_') {
			continue
		}
		return false
	}
	return true
}

func validDeviceID(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, character := range []byte(value) {
		if character < ' ' || character > '~' {
			return false
		}
	}
	return true
}

func validTarget(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '.' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func avdSystemImagePackage(profile *AVDProfile) string {
	return fmt.Sprintf("system-images;android-%d;%s;%s", profile.APILevel, profile.Target, profile.Arch)
}

func systemImageDirectory(root string, profile *AVDProfile) string {
	return filepath.Join(
		root,
		"system-images",
		"android-"+strconv.Itoa(profile.APILevel),
		profile.Target,
		profile.Arch,
	)
}

func systemImageInstalled(root string, profile *AVDProfile) bool {
	directory := systemImageDirectory(root, profile)
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return false
	}
	for _, marker := range []string{"package.xml", "source.properties", "system.img", "ramdisk.img"} {
		if markerInfo, markerErr := os.Stat(filepath.Join(directory, marker)); markerErr != nil || !markerInfo.Mode().IsRegular() {
			return false
		}
	}
	return true
}

func ensureSystemImage(
	ctx context.Context,
	root string,
	profile *AVDProfile,
	imagePackage string,
	deps avdDependencies,
) error {
	if systemImageInstalled(root, profile) {
		return nil
	}
	sdkmanager, findErr := deps.findTool(root, "sdkmanager")
	if findErr != nil {
		return fmt.Errorf("ensure AVD %q system image %q: %w", profile.Name, imagePackage, findErr)
	}
	installArgs := []string{"--sdk_root=" + root, "--install", imagePackage}
	licensesArgs := []string{"--sdk_root=" + root, "--licenses"}
	if !profile.InstallSystemImage {
		return fmt.Errorf(
			"ensure AVD %q: system image %q is not installed; install it explicitly with %s, accept licences interactively with %s, or set InstallSystemImage true after accepting licences",
			profile.Name,
			imagePackage,
			formatCommand(sdkmanager, installArgs),
			formatCommand(sdkmanager, licensesArgs),
		)
	}

	result, err := deps.run(ctx, sdkmanager, installArgs, nil, profile.Progress)
	if err != nil {
		installErr := commandFailure("install system image", sdkmanager, installArgs, result, err)
		return errors.Join(
			installErr,
			fmt.Errorf("android SDK licences are never accepted automatically; run %s interactively", formatCommand(sdkmanager, licensesArgs)),
		)
	}
	if !systemImageInstalled(root, profile) {
		return fmt.Errorf(
			"install system image %q: sdkmanager succeeded but %q is incomplete",
			imagePackage,
			systemImageDirectory(root, profile),
		)
	}
	return nil
}

func hardwareProfileAvailable(
	ctx context.Context,
	avdmanager string,
	profile *AVDProfile,
	run func(context.Context, string, []string, io.Reader, io.Writer) (androidsdk.CommandResult, error),
) (bool, error) {
	args := []string{"list", "device", "-c"}
	result, err := run(ctx, avdmanager, args, nil, nil)
	if err != nil {
		return false, commandFailure("list hardware profiles", avdmanager, args, result, err)
	}
	for _, line := range strings.Split(strings.ReplaceAll(result.Stdout, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == profile.Device {
			return true, nil
		}
	}
	return false, nil
}

func listAVDs(
	ctx context.Context,
	emulatorPath string,
	run func(context.Context, string, []string, io.Reader, io.Writer) (androidsdk.CommandResult, error),
) ([]string, error) {
	args := []string{"-list-avds"}
	result, err := run(ctx, emulatorPath, args, nil, nil)
	if err != nil {
		return nil, commandFailure("list AVDs", emulatorPath, args, result, err)
	}
	var avds []string
	for _, line := range strings.Split(strings.ReplaceAll(result.Stdout, "\r\n", "\n"), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			avds = append(avds, name)
		}
	}
	return avds, nil
}

func registeredAVDMatches(
	ctx context.Context,
	name string,
	homes []string,
	emulatorPath string,
	sdkRoot string,
	profile *AVDProfile,
	run func(context.Context, string, []string, io.Reader, io.Writer) (androidsdk.CommandResult, error),
) (bool, error) {
	listedAVDs, err := listAVDs(ctx, emulatorPath, run)
	if err != nil {
		return false, err
	}
	if !containsExactString(listedAVDs, name) {
		return false, nil
	}
	metadata, found, err := androidsdk.AVDConfig(name, homes)
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("emulator lists the AVD but its config.ini was not found")
	}
	if err := verifyAVDConfig(metadata.Values, sdkRoot, profile); err != nil {
		return false, err
	}
	return true, nil
}

func containsExactString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func verifyAVDConfig(values map[string]string, sdkRoot string, profile *AVDProfile) error {
	wantImageDirectory := systemImageDirectory(sdkRoot, profile)
	gotImageDirectory := configuredImageDirectory(sdkRoot, values["image.sysdir.1"])

	mismatches := make([]string, 0, 7)
	if got := strings.TrimSpace(values["AvdId"]); got != "" && got != profile.Name {
		mismatches = append(mismatches, fmt.Sprintf("AVD ID %q (want %q)", got, profile.Name))
	}
	if got := strings.TrimSpace(values["target"]); got != "" && got != "android-"+strconv.Itoa(profile.APILevel) {
		mismatches = append(mismatches, fmt.Sprintf("API target %q (want %q)", got, "android-"+strconv.Itoa(profile.APILevel)))
	}
	if !sameFilesystemPath(gotImageDirectory, wantImageDirectory) {
		mismatches = append(mismatches, fmt.Sprintf("system image directory %q (want %q)", gotImageDirectory, wantImageDirectory))
	}
	if got := strings.TrimSpace(values["hw.device.name"]); got != profile.Device {
		mismatches = append(mismatches, fmt.Sprintf("hardware profile %q (want %q)", got, profile.Device))
	}
	if got := strings.TrimSpace(values["hw.cpu.arch"]); got != "" && got != emulatorCPUArch(profile.Arch) {
		mismatches = append(mismatches, fmt.Sprintf("CPU architecture %q (want %q)", got, emulatorCPUArch(profile.Arch)))
	}
	if got := strings.TrimSpace(values["abi.type"]); got != "" && got != profile.Arch {
		mismatches = append(mismatches, fmt.Sprintf("ABI %q (want %q)", got, profile.Arch))
	}
	if got := strings.TrimSpace(values["tag.id"]); got != "" && got != profile.Target {
		mismatches = append(mismatches, fmt.Sprintf("target %q (want %q)", got, profile.Target))
	}
	if len(mismatches) > 0 {
		sort.Strings(mismatches)
		return fmt.Errorf(
			"existing AVD does not match the requested lightweight profile: %s; choose another Name or manage the existing AVD explicitly",
			strings.Join(mismatches, ", "),
		)
	}
	return nil
}

func configuredImageDirectory(sdkRoot, configured string) string {
	configured = filepath.FromSlash(strings.ReplaceAll(strings.TrimSpace(configured), "\\", "/"))
	if configured == "" {
		return ""
	}
	if !filepath.IsAbs(configured) {
		configured = filepath.Join(sdkRoot, configured)
	}
	return filepath.Clean(configured)
}

func sameFilesystemPath(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	left = canonicalPathWithMissingSuffix(left)
	right = canonicalPathWithMissingSuffix(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func canonicalPathWithMissingSuffix(path string) string {
	path = filepath.Clean(path)
	current := path
	var missing []string
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return resolved
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func emulatorCPUArch(abi string) string {
	if abi == "arm64-v8a" {
		return "arm64"
	}
	return abi
}

func commandFailure(
	action string,
	executable string,
	args []string,
	result androidsdk.CommandResult,
	err error,
) error {
	details := strings.TrimSpace(strings.Join(nonEmptyStrings(result.Stdout, result.Stderr), "\n"))
	if details == "" {
		return fmt.Errorf("%s with %s: %w", action, formatCommand(executable, args), err)
	}
	return fmt.Errorf("%s with %s: %w\noutput: %s", action, formatCommand(executable, args), err, details)
}

func nonEmptyStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result = append(result, strings.TrimSpace(value))
		}
	}
	return result
}

func formatCommand(executable string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteCommandArgument(executable))
	for _, arg := range args {
		parts = append(parts, quoteCommandArgument(arg))
	}
	return strings.Join(parts, " ")
}

func quoteCommandArgument(argument string) string {
	if runtime.GOOS == "windows" {
		return strconv.Quote(argument)
	}
	return "'" + strings.ReplaceAll(argument, "'", `'"'"'`) + "'"
}
