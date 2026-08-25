package androidsdk

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// These characterization tests moved here with findAVD, readRegularFile and
// parseINI. They pin the resolution paths EnsureAVD never reaches, because
// avdmanager always writes an absolute path entry.

func writeAVDMetadata(t *testing.T, home, name, body string) {
	t.Helper()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, name+".ini"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeAVDContent(t *testing.T, directory, config string) string {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "config.ini")
	if err := os.WriteFile(configPath, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestAVDConfigReturnsParsedValuesAndTheContentDirectory(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, "go_test.avd")
	writeAVDContent(t, directory, "AvdId=go_test\ndisk.dataPartition.size = 6442450944\n")
	writeAVDMetadata(t, home, "go_test", "path="+directory+"\n")

	metadata, found, err := AVDConfig("go_test", []string{home})
	if err != nil || !found {
		t.Fatalf("AVDConfig() = %+v, %v, %v", metadata, found, err)
	}
	if metadata.Directory != directory {
		t.Fatalf("AVDConfig() directory = %q, want %q", metadata.Directory, directory)
	}
	if metadata.Values["disk.dataPartition.size"] != "6442450944" || metadata.Values["AvdId"] != "go_test" {
		t.Fatalf("AVDConfig() values = %#v", metadata.Values)
	}
}

func TestAVDConfigReportsAnAbsentAVDWithoutError(t *testing.T) {
	metadata, found, err := AVDConfig("go_test", []string{t.TempDir()})
	if err != nil {
		t.Fatalf("AVDConfig() error: %v", err)
	}
	if found || metadata.Values != nil || metadata.Directory != "" {
		t.Fatalf("AVDConfig() = %+v, %v, want an absent AVD reported as not found", metadata, found)
	}
}

func TestFindAVDResolvesPathRelAgainstTheAVDHomeParent(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "avd")
	want := writeAVDContent(t, filepath.Join(home, "go_test.avd"), "AvdId=go_test\n")
	writeAVDMetadata(t, home, "go_test", "path.rel=avd/go_test.avd\n")

	_, got, found, err := findAVD("go_test", []string{home})
	if err != nil || !found {
		t.Fatalf("findAVD() = %q, %v, %v", got, found, err)
	}
	if got != want {
		t.Fatalf("findAVD() = %q, want %q", got, want)
	}
}

func TestFindAVDJoinsRelativePathAgainstTheAVDHome(t *testing.T) {
	home := t.TempDir()
	want := writeAVDContent(t, filepath.Join(home, "go_test.avd"), "AvdId=go_test\n")
	writeAVDMetadata(t, home, "go_test", "path=go_test.avd\n")

	_, got, found, err := findAVD("go_test", []string{home})
	if err != nil || !found {
		t.Fatalf("findAVD() = %q, %v, %v", got, found, err)
	}
	if got != want {
		t.Fatalf("findAVD() = %q, want %q", got, want)
	}
}

func TestFindAVDRejectsMetadataWithoutAPath(t *testing.T) {
	home := t.TempDir()
	writeAVDMetadata(t, home, "go_test", "target=android-35\n")

	_, _, found, err := findAVD("go_test", []string{home})
	if err == nil || !strings.Contains(err.Error(), "has no path") {
		t.Fatalf("findAVD() error = %v, want no-path rejection", err)
	}
	if found {
		t.Fatal("findAVD() reported a usable config for metadata without a path")
	}
}

func TestFindAVDReportsMetadataPointingAtAMissingConfig(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, "go_test.avd")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAVDMetadata(t, home, "go_test", "path="+directory+"\n")

	_, _, found, err := findAVD("go_test", []string{home})
	if err == nil || !strings.Contains(err.Error(), "points to missing config") {
		t.Fatalf("findAVD() error = %v, want missing-config rejection", err)
	}
	if found {
		t.Fatal("findAVD() reported a usable config that does not exist")
	}
}

func TestFindAVDRejectsNonRegularConfig(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, "go_test.avd")
	if err := os.MkdirAll(filepath.Join(directory, "config.ini"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeAVDMetadata(t, home, "go_test", "path="+directory+"\n")

	_, _, found, err := findAVD("go_test", []string{home})
	if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("findAVD() error = %v, want non-regular config rejection", err)
	}
	if found {
		t.Fatal("findAVD() reported a directory as a usable config")
	}
}

func TestFindAVDRejectsNonRegularMetadata(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "go_test.ini"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, _, found, err := findAVD("go_test", []string{home})
	if err == nil || !strings.Contains(err.Error(), "read AVD metadata") {
		t.Fatalf("findAVD() error = %v, want unreadable metadata rejection", err)
	}
	if found {
		t.Fatal("findAVD() reported a usable config for directory metadata")
	}
}

func TestFindAVDReportsUnreadableMetadata(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	home := t.TempDir()
	writeAVDMetadata(t, home, "go_test", "path=/anywhere\n")
	metadataPath := filepath.Join(home, "go_test.ini")
	if err := os.Chmod(metadataPath, 0o000); err != nil {
		t.Skipf("chmod is unsupported here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(metadataPath, 0o644) })

	_, _, found, err := findAVD("go_test", []string{home})
	if err == nil || !strings.Contains(err.Error(), "read AVD metadata") {
		t.Fatalf("findAVD() error = %v, want unreadable metadata rejection", err)
	}
	if found {
		t.Fatal("findAVD() reported a usable config it could not read")
	}
}

func TestFindAVDSkipsBlankHomesAndSearchesEveryHome(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	directory := filepath.Join(second, "go_test.avd")
	want := writeAVDContent(t, directory, "AvdId=go_test\n")
	writeAVDMetadata(t, second, "go_test", "path="+directory+"\n")

	_, got, found, err := findAVD("go_test", []string{"", "   ", first, second})
	if err != nil || !found {
		t.Fatalf("findAVD() = %q, %v, %v", got, found, err)
	}
	if got != want {
		t.Fatalf("findAVD() = %q, want %q", got, want)
	}
}

func TestFindAVDResolvesThroughASymlinkedAVDHome(t *testing.T) {
	base := t.TempDir()
	resolved := filepath.Join(base, "real")
	directory := filepath.Join(resolved, "go_test.avd")
	want := writeAVDContent(t, directory, "AvdId=go_test\n")
	writeAVDMetadata(t, resolved, "go_test", "path="+directory+"\n")
	link := filepath.Join(base, "link")
	if err := os.Symlink(resolved, link); err != nil {
		t.Skipf("symlinks are unsupported here: %v", err)
	}

	_, got, found, err := findAVD("go_test", []string{link})
	if err != nil || !found {
		t.Fatalf("findAVD() = %q, %v, %v", got, found, err)
	}
	if got != want {
		t.Fatalf("findAVD() = %q, want %q", got, want)
	}
}

func TestFindAVDUsesTheContentDirectoryNamedByMetadataNotTheAVDName(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, "Medium_Phone.avd")
	writeAVDContent(t, directory, "AvdId=Medium_Phone\n")
	writeAVDMetadata(t, home, "Medium_Phone_API_36.0", "path="+directory+"\n")

	gotDirectory, _, found, err := findAVD("Medium_Phone_API_36.0", []string{home})
	if err != nil || !found {
		t.Fatalf("findAVD() = %q, %v, %v", gotDirectory, found, err)
	}
	if gotDirectory != directory {
		t.Fatalf("findAVD() directory = %q, want %q", gotDirectory, directory)
	}
}

func TestReadRegularFileRejectsOversizeMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.ini")
	if err := os.WriteFile(path, make([]byte, maxAVDMetadataSize+1), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := readRegularFile(path); err == nil || !strings.Contains(err.Error(), "metadata limit") {
		t.Fatalf("readRegularFile() error = %v, want an oversize rejection", err)
	}
}

func TestParseINIHandlesCommentsWhitespaceAndMalformedLines(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  map[string]string
	}{
		{"comments and padding", "# hash\n; semicolon\n\n  key = value  \n", map[string]string{"key": "value"}},
		{"line without a separator", "novalue\nkey=value\n", map[string]string{"key": "value"}},
		{"duplicate key keeps the last", "key=first\nkey=second\n", map[string]string{"key": "second"}},
		{"empty value", "key=\n", map[string]string{"key": ""}},
		{"empty key", "=value\n", map[string]string{"": "value"}},
		{"carriage returns", "a=1\r\nb=2\r\n", map[string]string{"a": "1", "b": "2"}},
		{"separator inside the value", "key=a=b\n", map[string]string{"key": "a=b"}},
		{"emulator rewrite spacing", "disk.dataPartition.size = 6442450944\n", map[string]string{"disk.dataPartition.size": "6442450944"}},
		{"invalid utf-8 is preserved", "key=\xff\xfe\n", map[string]string{"key": "\xff\xfe"}},
		{"nul byte is preserved", "key=a\x00b\n", map[string]string{"key": "a\x00b"}},
		{"no trailing newline", "key=value", map[string]string{"key": "value"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseINI([]byte(test.input)); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseINI() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestAVDConfigReportsAnUnreadableConfigButStillNamesTheDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	home := t.TempDir()
	directory := filepath.Join(home, "go_test.avd")
	configPath := writeAVDContent(t, directory, "AvdId=go_test\n")
	writeAVDMetadata(t, home, "go_test", "path="+directory+"\n")
	if err := os.Chmod(configPath, 0o000); err != nil {
		t.Skipf("chmod is unsupported here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(configPath, 0o644) })

	metadata, found, err := AVDConfig("go_test", []string{home})
	if err == nil || !strings.Contains(err.Error(), "read AVD config") {
		t.Fatalf("AVDConfig() error = %v, want an unreadable-config rejection", err)
	}
	if !found || metadata.Values != nil {
		t.Fatalf("AVDConfig() = %+v, %v, want the AVD reported as present but unparsed", metadata, found)
	}
	if metadata.Directory != directory {
		t.Fatalf("AVDConfig() directory = %q, want %q so callers can still measure it", metadata.Directory, directory)
	}
}
