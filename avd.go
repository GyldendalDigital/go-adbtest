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
	"unicode"

	"github.com/GyldendalDigital/go-adbtest/internal/androidsdk"
)

const (
	defaultAVDDevice = "small_phone"
	defaultAVDTarget = "google_apis"
)

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
	// Arch is the system-image ABI. Empty selects the host-native accelerated ABI.
	Arch string
	// SDKRoot optionally selects an Android SDK. Empty uses ANDROID_HOME,
	// ANDROID_SDK_ROOT, or a root inferred from SDK tools on PATH.
	SDKRoot string
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
// explicitly true.
//
//nolint:gocritic // AVDProfile is a public value-style options struct by design.
func EnsureAVD(ctx context.Context, profile AVDProfile) (AVD, error) {
	return ensureAVDWithDependencies(ctx, profile, productionAVDDependencies())
}

type avdDependencies struct {
	hostArch       string
	resolveSDKRoot func(string) (string, error)
	findTool       func(string, string) (string, error)
	avdHomes       func() ([]string, error)
	run            func(context.Context, string, []string, io.Reader, io.Writer) (androidsdk.CommandResult, error)
}

func productionAVDDependencies() avdDependencies {
	return avdDependencies{
		hostArch:       runtime.GOARCH,
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
	if err := validateAVDDependencies(deps); err != nil {
		return AVD{}, err
	}

	normalized, err := normalizeAVDProfile(profile, deps.hostArch)
	if err != nil {
		return AVD{}, err
	}
	root, err := deps.resolveSDKRoot(normalized.SDKRoot)
	if err != nil {
		return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
	}
	normalized.SDKRoot = root
	imagePackage := avdSystemImagePackage(&normalized)
	result := AVD{Name: normalized.Name, SystemImage: imagePackage}

	homes, err := deps.avdHomes()
	if err != nil {
		return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
	}
	existingConfig, found, err := findAVDConfig(normalized.Name, homes)
	if err != nil {
		return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
	}
	if found {
		if err := verifyAVDConfig(existingConfig, &normalized); err != nil {
			return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
		}
		if err := ensureSystemImage(ctx, &normalized, imagePackage, deps); err != nil {
			return AVD{}, err
		}
		return result, nil
	}

	avdmanager, err := deps.findTool(root, "avdmanager")
	if err != nil {
		return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
	}
	if err := requireHardwareProfile(ctx, avdmanager, &normalized, deps.run); err != nil {
		return AVD{}, fmt.Errorf("ensure AVD %q: %w", normalized.Name, err)
	}
	if err := ensureSystemImage(ctx, &normalized, imagePackage, deps); err != nil {
		return AVD{}, err
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
		return AVD{}, commandFailure("create AVD", avdmanager, args, commandResult, err)
	}

	createdConfig, found, err := findAVDConfig(normalized.Name, homes)
	if err != nil {
		return AVD{}, fmt.Errorf("verify created AVD %q: %w", normalized.Name, err)
	}
	if !found {
		return AVD{}, fmt.Errorf(
			"verify created AVD %q: avdmanager succeeded but its config.ini was not found",
			normalized.Name,
		)
	}
	if err := verifyAVDConfig(createdConfig, &normalized); err != nil {
		return AVD{}, fmt.Errorf("verify created AVD %q: %w", normalized.Name, err)
	}
	result.Created = true
	return result, nil
}

func validateAVDDependencies(deps avdDependencies) error {
	if strings.TrimSpace(deps.hostArch) == "" || deps.resolveSDKRoot == nil || deps.findTool == nil ||
		deps.avdHomes == nil || deps.run == nil {
		return fmt.Errorf("ensure AVD: internal dependencies are incomplete")
	}
	return nil
}

//nolint:gocritic // Normalization returns an independent options value.
func normalizeAVDProfile(profile AVDProfile, hostArch string) (AVDProfile, error) {
	profile.Name = strings.TrimSpace(profile.Name)
	profile.Device = strings.TrimSpace(profile.Device)
	profile.Target = strings.TrimSpace(profile.Target)
	profile.Arch = strings.TrimSpace(profile.Arch)
	profile.SDKRoot = strings.TrimSpace(profile.SDKRoot)

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
	if !validProfileValue(profile.Device, true) {
		return AVDProfile{}, fmt.Errorf("ensure AVD %q: invalid hardware profile %q", profile.Name, profile.Device)
	}
	if profile.Target == "" {
		profile.Target = defaultAVDTarget
	}
	if !validProfileValue(profile.Target, false) {
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

	nativeArch, err := nativeAndroidArch(hostArch)
	if err != nil {
		return AVDProfile{}, fmt.Errorf("ensure AVD %q: %w", profile.Name, err)
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

func nativeAndroidArch(hostArch string) (string, error) {
	switch hostArch {
	case "amd64":
		return "x86_64", nil
	case "arm64":
		return "arm64-v8a", nil
	default:
		return "", fmt.Errorf("host architecture %q has no supported accelerated Android system image", hostArch)
	}
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

func validProfileValue(value string, allowSpace bool) bool {
	if value == "" || strings.ContainsAny(value, ";/\\") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || (!allowSpace && unicode.IsSpace(character)) {
			return false
		}
	}
	return true
}

func avdSystemImagePackage(profile *AVDProfile) string {
	return fmt.Sprintf("system-images;android-%d;%s;%s", profile.APILevel, profile.Target, profile.Arch)
}

func systemImageDirectory(profile *AVDProfile) string {
	return filepath.Join(
		profile.SDKRoot,
		"system-images",
		"android-"+strconv.Itoa(profile.APILevel),
		profile.Target,
		profile.Arch,
	)
}

func systemImageInstalled(profile *AVDProfile) bool {
	directory := systemImageDirectory(profile)
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return false
	}
	for _, marker := range []string{"package.xml", "source.properties"} {
		if markerInfo, markerErr := os.Stat(filepath.Join(directory, marker)); markerErr == nil && !markerInfo.IsDir() {
			return true
		}
	}
	return false
}

func ensureSystemImage(
	ctx context.Context,
	profile *AVDProfile,
	imagePackage string,
	deps avdDependencies,
) error {
	if systemImageInstalled(profile) {
		return nil
	}
	sdkmanager, findErr := deps.findTool(profile.SDKRoot, "sdkmanager")
	if findErr != nil {
		return fmt.Errorf("ensure AVD %q system image %q: %w", profile.Name, imagePackage, findErr)
	}
	installArgs := []string{"--sdk_root=" + profile.SDKRoot, "--install", imagePackage}
	licensesArgs := []string{"--sdk_root=" + profile.SDKRoot, "--licenses"}
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
	if !systemImageInstalled(profile) {
		return fmt.Errorf(
			"install system image %q: sdkmanager succeeded but %q is incomplete",
			imagePackage,
			systemImageDirectory(profile),
		)
	}
	return nil
}

func requireHardwareProfile(
	ctx context.Context,
	avdmanager string,
	profile *AVDProfile,
	run func(context.Context, string, []string, io.Reader, io.Writer) (androidsdk.CommandResult, error),
) error {
	args := []string{"list", "device", "-c"}
	result, err := run(ctx, avdmanager, args, nil, nil)
	if err != nil {
		return commandFailure("list hardware profiles", avdmanager, args, result, err)
	}
	for _, line := range strings.Split(strings.ReplaceAll(result.Stdout, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == profile.Device {
			return nil
		}
	}
	return fmt.Errorf(
		"hardware profile %q is unavailable; inspect valid IDs with %s",
		profile.Device,
		formatCommand(avdmanager, args),
	)
}

func findAVDConfig(name string, homes []string) (configPath string, found bool, err error) {
	for _, home := range homes {
		if strings.TrimSpace(home) == "" {
			continue
		}
		metadataPath := filepath.Join(home, name+".ini")
		// name is restricted to an ASCII AVD identifier and home comes from the
		// Android emulator's documented configuration locations.
		metadata, err := os.ReadFile(metadataPath) //nolint:gosec // Restricted AVD name under an Android-owned home.
		if err == nil {
			values := parseINI(metadata)
			avdPath := strings.TrimSpace(values["path"])
			if avdPath == "" {
				if relative := strings.TrimSpace(values["path.rel"]); relative != "" {
					avdPath = filepath.Join(filepath.Dir(home), filepath.FromSlash(relative))
				}
			}
			if avdPath == "" {
				return "", false, fmt.Errorf("AVD metadata %q has no path", metadataPath)
			}
			if !filepath.IsAbs(avdPath) {
				avdPath = filepath.Join(home, avdPath)
			}
			configPath := filepath.Join(filepath.Clean(avdPath), "config.ini")
			if _, statErr := os.Stat(configPath); statErr != nil {
				return "", false, fmt.Errorf("AVD metadata %q points to missing config %q: %w", metadataPath, configPath, statErr)
			}
			return configPath, true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", false, fmt.Errorf("read AVD metadata %q: %w", metadataPath, err)
		}

		directConfig := filepath.Join(home, name+".avd", "config.ini")
		if _, statErr := os.Stat(directConfig); statErr == nil {
			return directConfig, true, nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", false, fmt.Errorf("inspect AVD config %q: %w", directConfig, statErr)
		}
	}
	return "", false, nil
}

func verifyAVDConfig(configPath string, profile *AVDProfile) error {
	// configPath is resolved from validated AVD metadata, not arbitrary input.
	data, err := os.ReadFile(configPath) //nolint:gosec // Path was resolved from validated AVD metadata.
	if err != nil {
		return fmt.Errorf("read existing config %q: %w", configPath, err)
	}
	values := parseINI(data)
	wantImage := strings.ReplaceAll(avdSystemImagePackage(profile), ";", "/")
	gotImage := normalizeImagePath(values["image.sysdir.1"])

	mismatches := make([]string, 0, 4)
	if gotImage != wantImage {
		mismatches = append(mismatches, fmt.Sprintf("system image %q (want %q)", gotImage, wantImage))
	}
	if got := strings.TrimSpace(values["hw.device.name"]); got != profile.Device {
		mismatches = append(mismatches, fmt.Sprintf("hardware profile %q (want %q)", got, profile.Device))
	}
	if got := strings.TrimSpace(values["hw.cpu.arch"]); got != "" && got != profile.Arch {
		mismatches = append(mismatches, fmt.Sprintf("architecture %q (want %q)", got, profile.Arch))
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

func parseINI(data []byte) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}

func normalizeImagePath(path string) string {
	path = strings.Trim(strings.ReplaceAll(strings.TrimSpace(path), "\\", "/"), "/")
	if index := strings.Index(path, "system-images/"); index >= 0 {
		path = path[index:]
	}
	return path
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
