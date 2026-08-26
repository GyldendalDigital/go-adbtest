package androidsdk

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxAVDMetadataSize bounds the metadata this package reads. An AVD's
// config.ini is a few kilobytes; the cap keeps a read-only caller from loading
// an arbitrarily large file reached through an AVD's path entry.
const maxAVDMetadataSize = 1 << 20

// AVDMetadata describes an AVD located in one of the AVD homes.
type AVDMetadata struct {
	// Values is the parsed config.ini, nil when it could not be read.
	Values map[string]string
	// Directory is the AVD content directory holding the AVD's files. It is set
	// even when Values is not, so a caller can still inspect that location.
	Directory string
}

// AVDConfig locates the named AVD in the given AVD homes and returns its parsed
// config.ini together with the content directory holding it. It reports found
// false with a nil error when no home holds metadata for the name, and returns
// an error when metadata exists but is unusable.
func AVDConfig(name string, homes []string) (AVDMetadata, bool, error) {
	directory, configPath, found, err := findAVD(name, homes)
	if err != nil || !found {
		return AVDMetadata{Directory: directory}, found, err
	}
	data, err := readRegularFile(configPath)
	if err != nil {
		return AVDMetadata{Directory: directory}, true, fmt.Errorf("read AVD config %q: %w", configPath, err)
	}
	return AVDMetadata{Values: parseINI(data), Directory: directory}, true, nil
}

// findAVD resolves the AVD content directory and config.ini path for name by
// searching homes in order.
func findAVD(name string, homes []string) (directory, configPath string, found bool, err error) {
	for _, home := range homes {
		if strings.TrimSpace(home) == "" {
			continue
		}
		metadataPath := filepath.Join(home, name+".ini")
		metadata, err := readRegularFile(metadataPath)
		if err == nil {
			values := parseINI(metadata)
			avdPath := strings.TrimSpace(values["path"])
			if avdPath == "" {
				// path.rel is resolved against the AVD home's parent, which is
				// correct for the default ~/.android/avd layout. Android
				// defines it relative to ANDROID_EMULATOR_HOME, so a relocated
				// ANDROID_AVD_HOME can disagree. avdmanager always writes an
				// absolute path entry, so no tool in this project produces the
				// divergent case; the behaviour is pinned by test rather than
				// changed without a way to verify the alternative.
				if relative := strings.TrimSpace(values["path.rel"]); relative != "" {
					avdPath = filepath.Join(filepath.Dir(home), filepath.FromSlash(relative))
				}
			}
			if avdPath == "" {
				return "", "", false, fmt.Errorf("AVD metadata %q has no path", metadataPath)
			}
			if !filepath.IsAbs(avdPath) {
				avdPath = filepath.Join(home, avdPath)
			}
			directory := filepath.Clean(avdPath)
			configPath := filepath.Join(directory, "config.ini")
			// The directory travels with these errors: it is the location a
			// caller would still want to inspect or measure, and discarding it
			// would break AVDMetadata's promise to retain it when Values are
			// unavailable.
			if configInfo, statErr := os.Stat(configPath); statErr != nil {
				return directory, "", false, fmt.Errorf("AVD metadata %q points to missing config %q: %w", metadataPath, configPath, statErr)
			} else if !configInfo.Mode().IsRegular() {
				return directory, "", false, fmt.Errorf("AVD config %q is not a regular file", configPath)
			}
			return directory, configPath, true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", "", false, fmt.Errorf("read AVD metadata %q: %w", metadataPath, err)
		}
	}
	return "", "", false, nil
}

func readRegularFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", path)
	}
	if info.Size() > maxAVDMetadataSize {
		return nil, fmt.Errorf("%q is %d bytes, exceeding the %d-byte metadata limit", path, info.Size(), maxAVDMetadataSize)
	}
	return os.ReadFile(path) //nolint:gosec // Callers restrict paths to Android SDK/AVD metadata.
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
