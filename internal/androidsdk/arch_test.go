package androidsdk

import (
	"strings"
	"testing"
)

func TestNativeArch(t *testing.T) {
	tests := []struct {
		name    string
		goarch  string
		want    string
		wantErr string
	}{
		{name: "amd64", goarch: "amd64", want: "x86_64"},
		{name: "arm64", goarch: "arm64", want: "arm64-v8a"},
		{name: "unsupported", goarch: "riscv64", wantErr: "no supported accelerated"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NativeArch(test.goarch)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("NativeArch() error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NativeArch() error: %v", err)
			}
			if got != test.want {
				t.Fatalf("NativeArch() = %q, want %q", got, test.want)
			}
		})
	}
}
