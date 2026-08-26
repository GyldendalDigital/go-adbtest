package emulator

import (
	"strings"
	"sync"
	"testing"
)

func TestStartupLogKeepsTheEndWhenItOverflows(t *testing.T) {
	tests := []struct {
		name  string
		write int
	}{
		{"one byte under capacity", startupLogCapacity - 1},
		{"exactly capacity", startupLogCapacity},
		{"one byte over capacity", startupLogCapacity + 1},
		{"twice capacity in one write", startupLogCapacity * 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log := newStartupLog()
			payload := make([]byte, test.write)
			for index := range payload {
				payload[index] = byte('a' + index%26)
			}
			written, err := log.Write(payload)
			if written != test.write || err != nil {
				t.Fatalf("Write() = %d, %v, want %d, nil", written, err, test.write)
			}
			log.mu.Lock()
			held := len(log.buffer)
			last := byte(0)
			if held > 0 {
				last = log.buffer[held-1]
			}
			log.mu.Unlock()

			if held > startupLogCapacity {
				t.Fatalf("buffer grew to %d, want at most %d", held, startupLogCapacity)
			}
			// The useful line is the last one, so an overflow must discard the
			// oldest bytes rather than the newest.
			if last != payload[len(payload)-1] {
				t.Fatalf("last retained byte = %q, want %q", last, payload[len(payload)-1])
			}
		})
	}
}

func TestStartupLogDiscardsOldestAcrossManyWrites(t *testing.T) {
	log := newStartupLog()
	chunk := make([]byte, 4<<10)
	for index := range chunk {
		chunk[index] = 'x'
	}
	for written := 0; written < startupLogCapacity+(8<<10); written += len(chunk) {
		if _, err := log.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := log.Write([]byte("\nFATAL        | the last word\n")); err != nil {
		t.Fatal(err)
	}
	log.mu.Lock()
	held := len(log.buffer)
	log.mu.Unlock()
	if held > startupLogCapacity {
		t.Fatalf("buffer grew to %d, want at most %d", held, startupLogCapacity)
	}
	if !strings.Contains(log.Summary(), "the last word") {
		t.Fatalf("summary lost the newest line: %q", log.Summary())
	}
}

func TestStartupLogSummaryLiftsTheLastProblemLine(t *testing.T) {
	// The case this exists for: an unsupported -gpu value reports at line 6 of
	// more than a hundred and the emulator carries on, so a tail alone would
	// contain nothing actionable.
	log := newStartupLog()
	_, _ = log.Write([]byte("INFO         | Android emulator version 36.6.11.0\n"))
	_, _ = log.Write([]byte("WARNING      | Your AVD has been configured with an in-guest renderer, " +
		"but the system image does not support guest rendering.Falling back to 'lavapipe' mode.\n"))
	_, _ = log.Write([]byte("ERROR        | gpuChoiceBasedOnGpuOptions: Selected GPU option 'bogusmode' is not valid\n"))
	for index := 0; index < 60; index++ {
		_, _ = log.Write([]byte("INFO         | routine progress line\n"))
	}

	summary := log.Summary()
	if !strings.Contains(summary, "emulator reported: ") {
		t.Fatalf("summary has no problem line: %q", summary)
	}
	if !strings.Contains(summary, "bogusmode") {
		t.Fatalf("summary lost the ERROR line that scrolled out of the tail: %q", summary)
	}
	if !strings.Contains(summary, "routine progress line") {
		t.Fatalf("summary dropped the tail: %q", summary)
	}
}

func TestStartupLogSummaryPrefersTheLastProblem(t *testing.T) {
	log := newStartupLog()
	_, _ = log.Write([]byte("ERROR        | an earlier problem\n"))
	_, _ = log.Write([]byte("FATAL        | Not enough space to create userdata partition.\n"))

	if summary := log.Summary(); !strings.Contains(summary, "Not enough space") ||
		strings.Contains(summary, "emulator reported: ERROR        | an earlier problem") {
		t.Fatalf("summary = %q, want the last problem lifted", summary)
	}
}

func TestStartupLogSummaryIsEmptyWithoutOutput(t *testing.T) {
	if summary := newStartupLog().Summary(); summary != "" {
		t.Fatalf("Summary() = %q, want empty so an error gains no dangling header", summary)
	}
	log := newStartupLog()
	_, _ = log.Write([]byte("   \n\n\t\n"))
	if summary := log.Summary(); summary != "" {
		t.Fatalf("Summary() = %q, want whitespace-only output ignored", summary)
	}
}

func TestStartupLogKeepsAnUnterminatedFinalLine(t *testing.T) {
	log := newStartupLog()
	_, _ = log.Write([]byte("INFO         | first\nPANIC: no trailing newline"))

	if summary := log.Summary(); !strings.Contains(summary, "PANIC: no trailing newline") {
		t.Fatalf("summary dropped the unterminated final line: %q", summary)
	}
}

func TestStartupLogSanitizesControlBytes(t *testing.T) {
	log := newStartupLog()
	_, _ = log.Write([]byte("FATAL        | \x1b[31mred\x1b[0m\a and \x00 a nul\n"))

	summary := log.Summary()
	for _, forbidden := range []string{"\x1b", "\a", "\x00"} {
		if strings.Contains(summary, forbidden) {
			t.Fatalf("summary %q retains a control byte", summary)
		}
	}
	if !strings.Contains(summary, "red") {
		t.Fatalf("summary %q dropped the readable text", summary)
	}
}

func TestStartupLogSurvivesInvalidUTF8(t *testing.T) {
	log := newStartupLog()
	_, _ = log.Write([]byte("FATAL        | broken \xff\xfe bytes\n"))

	if summary := log.Summary(); !strings.Contains(summary, "broken") {
		t.Fatalf("summary = %q, want the line kept despite invalid UTF-8", summary)
	}
}

func TestStartupLogDropsTheADBPublicKey(t *testing.T) {
	// Logged twice per boot, carries the operating system user and host name,
	// and is about a fifth of a normal startup log.
	log := newStartupLog()
	_, _ = log.Write([]byte("INFO         | Sending adb public key [QAAAAL1LVndrOCGX" +
		strings.Repeat("A", 600) + " someuser@somehost]\n"))
	_, _ = log.Write([]byte("INFO         | androidboot.qemu.adb.pubkey=QAAAAL1LVndrOCGX" +
		strings.Repeat("A", 600) + " someuser@somehost\n"))
	_, _ = log.Write([]byte("FATAL        | Not enough space to create userdata partition.\n"))

	summary := log.Summary()
	if strings.Contains(summary, "someuser@somehost") || strings.Contains(summary, "pubkey") {
		t.Fatalf("summary leaked the adb public key: %q", summary)
	}
	if !strings.Contains(summary, "Not enough space") {
		t.Fatalf("summary lost the FATAL: %q", summary)
	}
}

func TestStartupLogCapsLineAndTailLength(t *testing.T) {
	log := newStartupLog()
	_, _ = log.Write([]byte("INFO         | " + strings.Repeat("wide ", 900) + "\n"))
	for index := 0; index < startupTailLines; index++ {
		_, _ = log.Write([]byte("INFO         | " + strings.Repeat("also wide ", 200) + "\n"))
	}

	summary := log.Summary()
	if len(summary) > startupTailBytes+startupLineBytes*2 {
		t.Fatalf("summary is %d bytes, want it capped near %d", len(summary), startupTailBytes)
	}
	for _, line := range strings.Split(summary, "\n") {
		if len(line) > startupLineBytes+len("emulator reported: ")+len("...") {
			t.Fatalf("line is %d bytes, want it capped near %d", len(line), startupLineBytes)
		}
	}
}

func TestStartupLogIsSafeUnderConcurrentUse(t *testing.T) {
	log := newStartupLog()
	var waitGroup sync.WaitGroup
	for writer := 0; writer < 4; writer++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for index := 0; index < 200; index++ {
				_, _ = log.Write([]byte("INFO         | concurrent line\n"))
			}
		}()
	}
	for index := 0; index < 200; index++ {
		_ = log.Summary()
	}
	waitGroup.Wait()
}

func TestStartupLogTolerationOfANilReceiver(t *testing.T) {
	var log *startupLog
	if written, err := log.Write([]byte("data")); written != 4 || err != nil {
		t.Fatalf("Write() on a nil log = %d, %v", written, err)
	}
	if summary := log.Summary(); summary != "" {
		t.Fatalf("Summary() on a nil log = %q", summary)
	}
}
