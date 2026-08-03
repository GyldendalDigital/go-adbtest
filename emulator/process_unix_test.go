//go:build unix

package emulator

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestStart_TimeoutKillsDescendantsAndReapsProcess(t *testing.T) {
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create readiness pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = readyReader.Close()
		_ = readyWriter.Close()
	})
	if err := readyReader.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set readiness deadline: %v", err)
	}

	deps := testStartDependencies()
	deviceCalls := 0
	descendantReady := false
	deps.devices = func(ctx context.Context) ([]string, error) {
		deviceCalls++
		if deviceCalls == 1 {
			return nil, nil
		}
		if deviceCalls == 2 {
			var ready [1]byte
			if _, err := io.ReadFull(readyReader, ready[:]); err != nil {
				return nil, err
			}
			if ready[0] != 'R' {
				return nil, errors.New("unexpected descendant readiness byte")
			}
			descendantReady = true
			return nil, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}

	var cmd *exec.Cmd
	deps.command = func(string, ...string) *exec.Cmd {
		cmd = exec.Command("sh", "-c", "(printf R >&3; while :; do sleep 60; done) & wait")
		cmd.ExtraFiles = []*os.File{readyWriter}
		return cmd
	}

	_, startErr := startWithDependencies(Config{AVD: "Test", Timeout: 250 * time.Millisecond}, deps)
	if cmd != nil && cmd.Process != nil {
		t.Cleanup(func() {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		})
	}
	if !errors.Is(startErr, context.DeadlineExceeded) {
		t.Fatalf("Start error = %v, want context deadline exceeded", startErr)
	}
	if !descendantReady {
		t.Fatal("emulator descendant did not start before cleanup")
	}
	if cmd == nil || cmd.ProcessState == nil {
		t.Fatal("timed-out emulator process was not reaped")
	}

	// Drop the test process's copy. EOF now requires every inherited copy in
	// the spawned process tree to be closed, proving no descendant survived.
	if err := readyWriter.Close(); err != nil {
		t.Fatalf("close readiness writer: %v", err)
	}
	if err := readyReader.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set descendant-exit deadline: %v", err)
	}
	var extra [1]byte
	n, err := readyReader.Read(extra[:])
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("read after cleanup = (%d, %v), want (0, EOF); a descendant may still be running", n, err)
	}
}
