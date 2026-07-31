package emulator

import (
	"testing"
)

func TestBuildArgs_AllOptions(t *testing.T) {
	cfg := Config{
		AVD:        "Pixel_7",
		Headless:   true,
		GPU:        "swiftshader_indirect",
		NoAudio:    true,
		WipeData:   true,
		NoSnapshot: true,
	}

	args := buildArgs(cfg)

	expected := map[string]bool{
		"-avd":                    true,
		"Pixel_7":                 true,
		"-no-boot-anim":          true,
		"-no-window":             true,
		"-gpu":                   true,
		"swiftshader_indirect":   true,
		"-no-audio":              true,
		"-wipe-data":             true,
		"-no-snapshot":           true,
	}

	for _, arg := range args {
		if !expected[arg] {
			t.Errorf("unexpected arg: %q", arg)
		}
		delete(expected, arg)
	}

	if len(expected) > 0 {
		t.Errorf("missing args: %v", expected)
	}
}

func TestBuildArgs_MinimalOptions(t *testing.T) {
	cfg := Config{
		AVD: "Pixel_7",
	}

	args := buildArgs(cfg)

	// Should have -avd Pixel_7 -no-boot-anim and nothing else
	if len(args) != 3 {
		t.Fatalf("buildArgs() = %v (len %d), want 3 args", args, len(args))
	}
	if args[0] != "-avd" || args[1] != "Pixel_7" || args[2] != "-no-boot-anim" {
		t.Errorf("buildArgs() = %v, want [-avd Pixel_7 -no-boot-anim]", args)
	}
}

func TestBuildArgs_GPUOnly(t *testing.T) {
	cfg := Config{
		AVD: "Test",
		GPU: "host",
	}

	args := buildArgs(cfg)

	hasGPU := false
	for i, arg := range args {
		if arg == "-gpu" && i+1 < len(args) && args[i+1] == "host" {
			hasGPU = true
		}
	}
	if !hasGPU {
		t.Errorf("buildArgs() = %v, missing -gpu host", args)
	}
}

func TestConfig_DefaultTimeout(t *testing.T) {
	cfg := Config{AVD: "Test"}
	if cfg.Timeout != 0 {
		t.Errorf("default Timeout = %v, want 0 (set by Start)", cfg.Timeout)
	}
}
