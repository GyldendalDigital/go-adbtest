// Command adbtest provides local diagnostics for go-adbtest consumers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"

	"github.com/GyldendalDigital/go-adbtest/internal/doctor"
)

type doctorCheck func(context.Context, doctor.Options) (doctor.Report, error)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	exitCode := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	os.Exit(exitCode)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runWithDoctor(ctx, args, stdout, stderr, doctor.Check)
}

func runWithDoctor(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	check doctorCheck,
) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "adbtest: a command is required")
		writeRootUsage(stderr)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		writeRootUsage(stdout)
		return 0
	case "doctor":
		return runDoctor(ctx, args[1:], stdout, stderr, check)
	default:
		_, _ = fmt.Fprintf(stderr, "adbtest: unknown command %q\n", args[0])
		writeRootUsage(stderr)
		return 2
	}
}

func runDoctor(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	check doctorCheck,
) int {
	if slices.Equal(args, []string{"-h"}) || slices.Equal(args, []string{"--help"}) {
		writeDoctorUsage(stdout)
		return 0
	}
	flags := flag.NewFlagSet("adbtest doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var options doctor.Options
	flags.StringVar(&options.AVD, "avd", "", "require an existing command-line AVD ID")
	flags.StringVar(&options.DeviceProfile, "device-profile", "small_phone", "required avdmanager hardware-profile ID")
	flags.Usage = func() { writeDoctorUsage(flags.Output()) }
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "adbtest doctor: unexpected argument %q\n", flags.Arg(0))
		writeDoctorUsage(stderr)
		return 2
	}
	if check == nil {
		_, _ = fmt.Fprintln(stderr, "adbtest doctor: internal checker is unavailable")
		return 1
	}
	report, err := check(ctx, options)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "adbtest doctor: %v\n", err)
		return 2
	}
	if err := report.Write(stdout); err != nil {
		_, _ = fmt.Fprintf(stderr, "adbtest doctor: write report: %v\n", err)
		return 1
	}
	return report.ExitCode()
}

func writeRootUsage(writer io.Writer) {
	_, _ = fmt.Fprintln(writer, "Usage: adbtest <command> [options]")
	_, _ = fmt.Fprintln(writer)
	_, _ = fmt.Fprintln(writer, "Commands:")
	_, _ = fmt.Fprintln(writer, "  doctor    diagnose owned-emulator prerequisites without changing the host")
}

func writeDoctorUsage(writer io.Writer) {
	_, _ = fmt.Fprintln(writer, "Usage: adbtest doctor [options]")
	_, _ = fmt.Fprintln(writer)
	_, _ = fmt.Fprintln(writer, "Options:")
	_, _ = fmt.Fprintln(writer, "  --avd NAME                 require an existing command-line AVD ID")
	_, _ = fmt.Fprintln(writer, "  --device-profile PROFILE   required hardware profile (default small_phone)")
	_, _ = fmt.Fprintln(writer, "  -h, --help                 show this help")
}
