package emulator

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode"
)

const (
	// startupLogCapacity bounds what is retained from the emulator's own
	// output. A complete default headless boot is about 8.7 KiB, so the ring
	// normally holds the entire startup log rather than a window of it; a
	// -verbose boot is about 115 KiB and does wrap.
	startupLogCapacity = 64 << 10
	// startupTailLines is how much of the tail an error carries. Every hard
	// startup failure measured against emulator 36.6.11.0 was 16 lines or
	// fewer in total, so this is the whole log for the cases that matter.
	startupTailLines = 20
	// startupTailBytes caps the tail independently of the line count, because
	// a -verbose line can exceed 3 KiB on its own.
	startupTailBytes = 4 << 10
	// startupLineBytes caps one rendered line.
	startupLineBytes = 200
)

// startupLog retains the tail of an emulator's stdout and stderr so a startup
// failure can carry the emulator's own explanation. Both reader goroutines
// write into it concurrently, and a failing caller reads it, so every access is
// guarded.
type startupLog struct {
	mu     sync.Mutex
	buffer []byte
}

func newStartupLog() *startupLog {
	return &startupLog{buffer: make([]byte, 0, startupLogCapacity)}
}

// Write appends to the ring, discarding the oldest bytes once it is full. It
// never grows, never blocks and never fails, so a reader goroutine can call it
// before teeing on to a writer that might block.
func (l *startupLog) Write(data []byte) (int, error) {
	if l == nil {
		return len(data), nil
	}
	written := len(data)
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(data) >= startupLogCapacity {
		// Keep the end: the useful line is the last one.
		data = data[len(data)-startupLogCapacity:]
		l.buffer = append(l.buffer[:0], data...)
		return written, nil
	}
	if overflow := len(l.buffer) + len(data) - startupLogCapacity; overflow > 0 {
		l.buffer = append(l.buffer[:0], l.buffer[overflow:]...)
	}
	l.buffer = append(l.buffer, data...)
	return written, nil
}

// Summary renders what an error should carry: the last line the emulator
// marked ERROR or FATAL wherever it appeared, followed by the tail.
//
// The tail alone is not enough. Every hard failure puts its FATAL last, but a
// misconfiguration such as an unsupported -gpu value reports at line 6 of more
// than a hundred and then carries on booting, so a tail of 20 would contain
// nothing actionable.
func (l *startupLog) Summary() string {
	lines := l.lines()
	if len(lines) == 0 {
		return ""
	}
	var sections []string
	if problem := lastProblem(lines); problem != "" {
		sections = append(sections, "emulator reported: "+problem)
	}
	if tail := renderTail(lines); tail != "" {
		sections = append(sections, "last emulator output:\n"+tail)
	}
	return strings.Join(sections, "\n")
}

func (l *startupLog) lines() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	raw := string(l.buffer)
	l.mu.Unlock()

	split := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(split))
	for _, line := range split {
		if line = sanitizeLine(line); line != "" && !isNoisyLine(line) {
			lines = append(lines, line)
		}
	}
	// A wrap can bisect the first line, leaving a fragment that reads as if the
	// emulator said something it did not.
	if len(lines) > 0 && len(raw) >= startupLogCapacity {
		lines = lines[1:]
	}
	return lines
}

// lastProblem returns the last line the emulator's own logger marked as a
// problem. The format is a fixed "LEVEL        | message".
func lastProblem(lines []string) string {
	for index := len(lines) - 1; index >= 0; index-- {
		level, _, found := strings.Cut(lines[index], "|")
		if !found {
			continue
		}
		switch strings.TrimSpace(level) {
		case "FATAL", "ERROR":
			return lines[index]
		}
	}
	return ""
}

func renderTail(lines []string) string {
	if len(lines) > startupTailLines {
		lines = lines[len(lines)-startupTailLines:]
	}
	// Drop from the front until the tail fits, so the newest lines - where a
	// failure reports - always survive the cap.
	total, start := 0, len(lines)
	for index := len(lines) - 1; index >= 0; index-- {
		total += len(lines[index]) + 1
		if total > startupTailBytes {
			break
		}
		start = index
	}
	return strings.Join(lines[start:], "\n")
}

// isNoisyLine drops output that is long, repeated and never diagnostic. The adb
// public key is logged twice per boot, carries the operating system user and
// host name, and accounts for roughly a fifth of a normal startup log.
func isNoisyLine(line string) bool {
	return strings.Contains(line, "Sending adb public key") ||
		strings.Contains(line, "androidboot.qemu.adb.pubkey=")
}

// sanitizeLine trims a line and replaces anything non-printing, so an error is
// safe to write to a terminal or a log. Real emulator output contains no
// control bytes at all; this guards against a wedged process emitting them.
func sanitizeLine(line string) string {
	line = strings.TrimRight(line, " \t")
	if len(line) > startupLineBytes {
		line = line[:startupLineBytes] + "..."
	}
	return strings.TrimSpace(strings.Map(func(character rune) rune {
		if character == '\t' {
			return ' '
		}
		if unicode.IsPrint(character) {
			return character
		}
		return -1
	}, line))
}

// outputCapture owns the pipes handed to the emulator and the goroutines that
// drain them.
type outputCapture struct {
	log          *startupLog
	drained      chan struct{}
	stdoutWriter *os.File
	stderrWriter *os.File
	stdoutReader *os.File
	stderrReader *os.File
	waitGroup    sync.WaitGroup
}

func newCapture(output, errOutput io.Writer) (*outputCapture, error) {
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create emulator output pipe: %w", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return nil, fmt.Errorf("create emulator error pipe: %w", err)
	}
	capture := &outputCapture{
		log:          newStartupLog(),
		drained:      make(chan struct{}),
		stdoutWriter: stdoutWriter,
		stderrWriter: stderrWriter,
		stdoutReader: stdoutReader,
		stderrReader: stderrReader,
	}
	capture.waitGroup.Add(2)
	go capture.drain(stdoutReader, output)
	go capture.drain(stderrReader, errOutput)
	go func() {
		capture.waitGroup.Wait()
		close(capture.drained)
	}()
	return capture, nil
}

// newDetachedCapture serves an Instance built without a started process, so the
// failure paths find a usable log and an already-closed drained channel rather
// than nils.
func newDetachedCapture() *outputCapture {
	drained := make(chan struct{})
	close(drained)
	return &outputCapture{log: newStartupLog(), drained: drained}
}

// drain copies one stream into the ring and then on to the live writer.
//
// The ring first, deliberately. Writing to it is a memcpy that cannot block,
// while the live writer may be a terminal or a CI pipe; a reader parked in that
// write leaves the emulator's last words sitting unread in the kernel pipe, and
// measured with a slow writer that lost the FATAL on every single run.
func (c *outputCapture) drain(reader *os.File, live io.Writer) {
	defer c.waitGroup.Done()
	defer func() { _ = reader.Close() }()
	buffer := make([]byte, 32<<10)
	for {
		read, err := reader.Read(buffer)
		if read > 0 {
			_, _ = c.log.Write(buffer[:read])
			if live != nil {
				_, _ = live.Write(buffer[:read])
			}
		}
		if err != nil {
			return
		}
	}
}

// closeWriters releases this process's copies of the write ends. Until they are
// closed the pipes cannot reach EOF, however long ago the emulator exited.
func (c *outputCapture) closeWriters() {
	if c.stdoutWriter != nil {
		_ = c.stdoutWriter.Close()
	}
	if c.stderrWriter != nil {
		_ = c.stderrWriter.Close()
	}
}

func (c *outputCapture) readEnds() []*os.File {
	ends := make([]*os.File, 0, 2)
	for _, end := range []*os.File{c.stdoutReader, c.stderrReader} {
		if end != nil {
			ends = append(ends, end)
		}
	}
	return ends
}
