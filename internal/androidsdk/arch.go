package androidsdk

import "fmt"

// NativeArch maps a Go host architecture to the Android system-image ABI that
// can use host VM acceleration.
func NativeArch(goarch string) (string, error) {
	switch goarch {
	case "amd64":
		return "x86_64", nil
	case "arm64":
		return "arm64-v8a", nil
	default:
		return "", fmt.Errorf("host architecture %q has no supported accelerated Android system image", goarch)
	}
}
