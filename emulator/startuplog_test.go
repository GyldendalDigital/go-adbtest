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
			// Asserting the exact tail, not merely its last byte: an off-by-one
			// in the discard arithmetic keeps the right final byte while losing
			// one from the front on every wrap.
			wantHeld := test.write
			if wantHeld > startupLogCapacity {
				wantHeld = startupLogCapacity
			}
			if held != wantHeld {
				t.Fatalf("retained %d bytes, want exactly %d", held, wantHeld)
			}
			log.mu.Lock()
			retained := string(log.buffer)
			log.mu.Unlock()
			if want := string(payload[len(payload)-wantHeld:]); retained != want {
				t.Fatalf("retained bytes differ from the expected tail")
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

// A real boot emits benign errors late - graphics init, then shutdown - so
// taking the last error of any kind names a line that had nothing to do with
// the failure. The line that matters here is the sixth of more than a hundred.
func TestStartupLogSummaryLiftsTheProblemPastLaterBenignErrors(t *testing.T) {
	log := newStartupLog()
	_, _ = log.Write([]byte("INFO         | Android emulator version 36.6.11.0\n"))
	_, _ = log.Write([]byte("ERROR        | gpuChoiceBasedOnGpuOptions: Selected GPU option 'bogusmode' is not valid, switching to 'auto' mode.\n"))
	for index := 0; index < 40; index++ {
		_, _ = log.Write([]byte("INFO         | routine progress line\n"))
	}
	// Emitted on every boot of this emulator, including with no -gpu flag.
	_, _ = log.Write([]byte("ERROR        | Setting read-only feature 'GLAsyncSwap' to '0'\n"))
	for index := 0; index < 40; index++ {
		_, _ = log.Write([]byte("INFO         | more progress\n"))
	}
	// Emitted on every graceful shutdown.
	_, _ = log.Write([]byte("ERROR        | adb protocol fault (couldn't read status length)\n"))
	_, _ = log.Write([]byte("ERROR        | stop: Not implemented\n"))

	summary := log.Summary()
	if !strings.Contains(summary, "emulator reported: ") {
		t.Fatalf("summary has no problem line: %q", summary)
	}
	if !strings.Contains(summary, "bogusmode") {
		t.Fatalf("summary named a later benign error instead of the cause: %q", summary)
	}
	for _, benign := range []string{"GLAsyncSwap", "stop: Not implemented", "adb protocol fault"} {
		if strings.Contains(summary, "emulator reported: ERROR        | "+benign) {
			t.Fatalf("summary lifted the benign line %q: %s", benign, summary)
		}
	}
}

func TestStartupLogSummaryPrefersAFatalOverAnEarlierError(t *testing.T) {
	log := newStartupLog()
	_, _ = log.Write([]byte("ERROR        | an earlier non-terminal problem\n"))
	for index := 0; index < 40; index++ {
		_, _ = log.Write([]byte("INFO         | progress\n"))
	}
	_, _ = log.Write([]byte("FATAL        | Not enough space to create userdata partition.\n"))
	for index := 0; index < 40; index++ {
		_, _ = log.Write([]byte("INFO         | more progress\n"))
	}

	summary := log.Summary()
	if !strings.Contains(summary, "emulator reported: FATAL") ||
		!strings.Contains(summary, "Not enough space") {
		t.Fatalf("summary = %q, want the FATAL preferred", summary)
	}
}

func TestStartupLogSummaryPrefersTheLastFatal(t *testing.T) {
	log := newStartupLog()
	_, _ = log.Write([]byte("FATAL        | an earlier fatal\n"))
	for index := 0; index < 40; index++ {
		_, _ = log.Write([]byte("INFO         | progress\n"))
	}
	_, _ = log.Write([]byte("FATAL        | the terminal one\n"))
	for index := 0; index < 40; index++ {
		_, _ = log.Write([]byte("INFO         | more progress\n"))
	}

	if summary := log.Summary(); !strings.Contains(summary, "emulator reported: FATAL        | the terminal one") {
		t.Fatalf("summary = %q, want the last FATAL", summary)
	}
}

func TestStartupLogSummaryLiftsNothingFromABenignRun(t *testing.T) {
	log := newStartupLog()
	_, _ = log.Write([]byte("ERROR        | Setting read-only feature 'GLAsyncSwap' to '0'\n"))
	_, _ = log.Write([]byte("ERROR        | stop: Not implemented\n"))

	if summary := log.Summary(); strings.Contains(summary, "emulator reported:") {
		t.Fatalf("summary = %q, want no diagnosis from benign errors alone", summary)
	}
}

// A hard failure is short enough that the tail is the whole log, so lifting its
// last line would print the same text twice.
func TestStartupLogSummaryDoesNotRepeatTheLastLine(t *testing.T) {
	log := newStartupLog()
	_, _ = log.Write([]byte("INFO         | Android emulator version 36.6.11.0\n"))
	_, _ = log.Write([]byte("FATAL        | Not enough space to create userdata partition.\n"))

	summary := log.Summary()
	if strings.Contains(summary, "emulator reported:") {
		t.Fatalf("summary = %q, want no lifted line when it is already the tail's last", summary)
	}
	if strings.Count(summary, "Not enough space") != 1 {
		t.Fatalf("summary = %q, want the FATAL exactly once", summary)
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

func TestStartupLogRetainsTheExactTailAcrossIncrementalWrites(t *testing.T) {
	log := newStartupLog()
	var expected []byte
	chunk := make([]byte, 1<<10)
	for round := 0; round < (startupLogCapacity/len(chunk))+8; round++ {
		for index := range chunk {
			chunk[index] = byte('A' + (round+index)%26)
		}
		if _, err := log.Write(chunk); err != nil {
			t.Fatal(err)
		}
		expected = append(expected, chunk...)
	}
	if len(expected) > startupLogCapacity {
		expected = expected[len(expected)-startupLogCapacity:]
	}
	log.mu.Lock()
	retained := string(log.buffer)
	log.mu.Unlock()
	// No newlines in this payload, so nothing is trimmed to a line boundary.
	if retained != string(expected) {
		t.Fatalf("retained %d bytes, want the exact newest %d", len(retained), len(expected))
	}
}

// A wrap must not leave a fragment that reads as a line the emulator never
// wrote.
func TestStartupLogDiscardsAPartialLeadingLine(t *testing.T) {
	log := newStartupLog()
	filler := strings.Repeat("INFO         | filler line that is reasonably long\n", 2000)
	if _, err := log.Write([]byte(filler)); err != nil {
		t.Fatal(err)
	}
	if _, err := log.Write([]byte("FATAL        | the end\n")); err != nil {
		t.Fatal(err)
	}
	log.mu.Lock()
	first := strings.SplitN(string(log.buffer), "\n", 2)[0]
	log.mu.Unlock()

	if first != "" && !strings.HasPrefix(first, "INFO         |") {
		t.Fatalf("retained log begins mid-line: %q", first)
	}
}

func TestRenderTailKeepsExactlyTheLineLimit(t *testing.T) {
	lines := make([]string, 0, startupTailLines+1)
	for index := 0; index <= startupTailLines; index++ {
		lines = append(lines, "INFO         | line")
	}
	if got := strings.Count(renderTail(lines), "\n") + 1; got != startupTailLines {
		t.Fatalf("renderTail kept %d lines, want %d", got, startupTailLines)
	}
}

func TestRenderTailAppliesTheByteCap(t *testing.T) {
	lines := make([]string, 0, startupTailLines)
	for index := 0; index < startupTailLines; index++ {
		lines = append(lines, strings.Repeat("w", startupLineBytes))
	}
	rendered := renderTail(lines)
	if len(rendered) > startupTailBytes {
		t.Fatalf("renderTail returned %d bytes, want at most %d", len(rendered), startupTailBytes)
	}
	if rendered == "" {
		t.Fatal("renderTail returned nothing")
	}
}

func TestSanitizeLineReplacesTabsAndCapsLength(t *testing.T) {
	if got := sanitizeLine("FATAL | left\tright"); got != "FATAL | left right" {
		t.Fatalf("sanitizeLine() = %q, want the tab rendered as a space", got)
	}
	// The cap is applied after replacement, because one invalid byte becomes a
	// three-byte replacement rune.
	long := sanitizeLine(strings.Repeat("\xff", startupLineBytes))
	if len(long) > startupLineBytes+len("...") {
		t.Fatalf("sanitizeLine() returned %d bytes, want it capped near %d", len(long), startupLineBytes)
	}
}
