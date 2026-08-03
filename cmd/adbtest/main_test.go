package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GyldendalDigital/go-adbtest/internal/doctor"
)

func TestRunDoctorReturnsReportExitCodeAndOptions(t *testing.T) {
	var got doctor.Options
	check := func(_ context.Context, options doctor.Options) (doctor.Report, error) {
		got = options
		return doctor.Report{Results: []doctor.Result{{
			Severity: doctor.Failure,
			Name:     "required",
			Detail:   "missing",
		}}}, nil
	}
	var stdout, stderr bytes.Buffer
	exitCode := runWithDoctor(
		context.Background(),
		[]string{"doctor", "--avd", "test_avd", "--device-profile", "small_phone"},
		&stdout,
		&stderr,
		check,
	)
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if got.AVD != "test_avd" || got.DeviceProfile != "small_phone" {
		t.Fatalf("options = %+v", got)
	}
	if !strings.Contains(stdout.String(), "Not ready") || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func TestRunHelpAndUsageErrors(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{name: "root help", args: []string{"--help"}, wantCode: 0, wantOut: "Usage: adbtest"},
		{name: "doctor help", args: []string{"doctor", "--help"}, wantCode: 0, wantOut: "Usage: adbtest doctor"},
		{name: "missing command", wantCode: 2, wantErr: "command is required"},
		{name: "unknown command", args: []string{"unknown"}, wantCode: 2, wantErr: "unknown command"},
		{name: "extra argument", args: []string{"doctor", "extra"}, wantCode: 2, wantErr: "unexpected argument"},
		{name: "unknown flag", args: []string{"doctor", "--unknown"}, wantCode: 2, wantErr: "flag provided but not defined"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			called := false
			check := func(context.Context, doctor.Options) (doctor.Report, error) {
				called = true
				return doctor.Report{}, nil
			}
			got := runWithDoctor(context.Background(), test.args, &stdout, &stderr, check)
			if got != test.wantCode {
				t.Fatalf("exit code = %d, want %d", got, test.wantCode)
			}
			if test.wantOut != "" && !strings.Contains(stdout.String(), test.wantOut) {
				t.Fatalf("stdout %q does not contain %q", stdout.String(), test.wantOut)
			}
			if test.wantErr != "" && !strings.Contains(stderr.String(), test.wantErr) {
				t.Fatalf("stderr %q does not contain %q", stderr.String(), test.wantErr)
			}
			if called {
				t.Fatal("checker was called for help or invalid usage")
			}
		})
	}
}

func TestRunDoctorCheckError(t *testing.T) {
	check := func(context.Context, doctor.Options) (doctor.Report, error) {
		return doctor.Report{}, errors.New("invalid options")
	}
	var stdout, stderr bytes.Buffer
	got := runWithDoctor(context.Background(), []string{"doctor"}, &stdout, &stderr, check)
	if got != 2 || !strings.Contains(stderr.String(), "invalid options") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", got, stdout.String(), stderr.String())
	}
}
