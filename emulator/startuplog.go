package emulator

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// startupLogCapacity bounds what is retained from the emulator's own
	// output. A complete default headless boot measures 9.1 to 9.3 KiB, so the
	// ring normally holds the entire startup log rather than a window of it; a
	// -verbose boot is about 110 KiB and does wrap.
	startupLogCapacity = 64 << 10
	// startupTailLines is how much of the tail an error carries. Every hard
	// startup failure measured against emulator 36.6.11.0 was 18 lines or
	// fewer in total, so this is close to the whole log for the cases that
	// matter. Not every one ends in a FATAL: a missing AVD reports only errors.
	startupTailLines = 20
	// startupTailBytes caps the tail independently of the line count. It is
	// below startupTailLines x startupLineBytes on purpose: a tail of twenty
	// wide lines is already more than anyone reads in an error, and lines that
	// wide are progress noise rather than diagnosis.
	startupTailBytes = 2 << 10
	// startupLineBytes caps one rendered line.
	startupLineBytes = 200
	// liveTeeDepth is how far the live copy may fall behind the reader before
	// chunks start being dropped. A default boot is nine or ten reads, so this
	// only bites when the writer has genuinely stalled.
	liveTeeDepth = 16
	// liveTeeFlushWait bounds how long teardown waits for the live writer.
	liveTeeFlushWait = 250 * time.Millisecond
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
		data = afterFirstNewline(data)
		l.buffer = append(l.buffer[:0], data...)
		return written, nil
	}
	if overflow := len(l.buffer) + len(data) - startupLogCapacity; overflow > 0 {
		// Discard through the end of the partial line as well, so what is
		// retained always begins at a line boundary. Otherwise the oldest entry
		// is a fragment that reads as something the emulator did not say.
		remainder := afterFirstNewline(l.buffer[overflow:])
		l.buffer = append(l.buffer[:0], remainder...)
	}
	l.buffer = append(l.buffer, data...)
	return written, nil
}

// Summary renders what an error should carry: the line most likely to explain
// the failure, followed by the tail.
//
// The tail alone is not enough. Hard failures put their explanation at the end,
// but a misconfiguration such as an unsupported -gpu value reports at line 6 of
// more than a hundred and then carries on booting, so a tail of 20 would
// contain nothing actionable.
//
// Note that the two drain goroutines write into this ring independently, so a
// stdout and a stderr chunk can interleave mid-line. In practice the emulator
// writes one 112-byte line to stderr per boot, during graphics init and far
// from any failure.
func (l *startupLog) Summary() string {
	lines := l.lines()
	if len(lines) == 0 {
		return ""
	}
	tail := renderTail(lines)
	problem := lastProblem(lines)
	// Every hard failure is short enough that the tail is the whole log, so
	// lifting a line that is already its last one just prints it twice.
	if problem != "" && strings.HasSuffix(tail, problem) {
		problem = ""
	}
	var sections []string
	if problem != "" {
		sections = append(sections, "emulator reported: "+problem)
	}
	if tail != "" {
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
	return lines
}

// lastProblem returns the line most likely to explain a failure: the last
// FATAL if there is one, and otherwise the FIRST error.
//
// Taking the last error of any kind does not survive contact with a real
// emulator. It emits benign errors late in every boot - "Setting read-only
// feature" during graphics init, and on shutdown "adb protocol fault" and
// "stop: Not implemented" - so an unsupported -gpu value reporting at line 6
// always lost to them, and the summary named a line that had nothing to do with
// the failure. That is worse than saying nothing, because it reads as a
// diagnosis.
//
// FATAL is terminal, so the last one is the one that stopped the emulator. An
// error is not terminal, so the earliest is the one that started the trouble.
func lastProblem(lines []string) string {
	var lastFatal, firstError string
	for _, line := range lines {
		level, message, found := strings.Cut(line, "|")
		if !found {
			continue
		}
		switch strings.TrimSpace(level) {
		case "FATAL":
			lastFatal = line
		case "ERROR":
			if firstError == "" && !isBenignError(message) {
				firstError = line
			}
		}
	}
	if lastFatal != "" {
		return lastFatal
	}
	return firstError
}

// isBenignError filters the errors emulator 36.6.11.0 emits on a healthy run,
// which would otherwise be reported as the cause of an unrelated failure.
func isBenignError(message string) bool {
	for _, benign := range []string{
		"Setting read-only feature",
		"stop: Not implemented",
		"adb protocol fault",
	} {
		if strings.Contains(message, benign) {
			return true
		}
	}
	return false
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
// public key is logged exactly twice per boot, carries the operating system
// user and host name, and measures 1529 of 9279 bytes - about a sixth of a
// normal startup log.
func isNoisyLine(line string) bool {
	return strings.Contains(line, "Sending adb public key") ||
		strings.Contains(line, "androidboot.qemu.adb.pubkey=")
}

// sanitizeLine trims a line and replaces anything non-printing, so an error is
// safe to write to a terminal or a log. Real emulator output contains no
// control bytes at all; this guards against a wedged process emitting them.
func sanitizeLine(line string) string {
	cleaned := strings.TrimSpace(strings.Map(func(character rune) rune {
		if character == '\t' {
			return ' '
		}
		if unicode.IsPrint(character) {
			return character
		}
		return -1
	}, line))
	return truncateRunes(cleaned, startupLineBytes)
}

// truncateRunes caps a string by bytes without bisecting a rune. The cap is
// applied after sanitising, because replacing one invalid byte yields a
// three-byte replacement rune and a byte cap taken beforehand would not hold.
func truncateRunes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	end := 0
	for index := range value {
		if index > limit {
			break
		}
		end = index
	}
	return value[:end] + "..."
}

// afterFirstNewline returns what follows the first newline, or the input
// unchanged when it holds no newline or nothing would remain.
func afterFirstNewline(data []byte) []byte {
	if index := bytes.IndexByte(data, '\n'); index >= 0 && index+1 < len(data) {
		return data[index+1:]
	}
	return data
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

// drain copies one stream into the ring and hands it on to the live writer.
//
// The ring first, and the live copy through a bounded queue rather than
// directly. Writing to the ring is a memcpy that cannot block; the live writer
// may be a terminal or a CI pipe and can stall for longer than any grace this
// package is willing to wait. A reader parked in that write leaves the
// emulator's last words sitting unread in the kernel pipe, and they are then
// lost when the reader is stopped - measured losing the FATAL on every run.
//
// So the live copy is the one allowed to suffer: it is dropped under back
// pressure, and what was dropped is still in the ring and still in the error.
func (c *outputCapture) drain(reader *os.File, live io.Writer) {
	defer c.waitGroup.Done()
	defer func() { _ = reader.Close() }()
	tee := newLiveTee(live)
	defer tee.close()
	buffer := make([]byte, 32<<10)
	for {
		read, err := reader.Read(buffer)
		if read > 0 {
			_, _ = c.log.Write(buffer[:read])
			tee.send(buffer[:read])
		}
		if err != nil {
			return
		}
	}
}

// liveTee forwards to the live writer without ever blocking its caller.
type liveTee struct {
	chunks chan []byte
	done   chan struct{}
}

func newLiveTee(live io.Writer) *liveTee {
	tee := &liveTee{chunks: make(chan []byte, liveTeeDepth), done: make(chan struct{})}
	go func() {
		defer close(tee.done)
		for chunk := range tee.chunks {
			if live != nil {
				_, _ = live.Write(chunk)
			}
		}
	}()
	return tee
}

// send queues a copy of data, discarding it if the writer has fallen behind.
func (t *liveTee) send(data []byte) {
	chunk := make([]byte, len(data))
	copy(chunk, data)
	select {
	case t.chunks <- chunk:
	default:
	}
}

// close stops the tee and waits briefly for it to flush, so ordinary output has
// landed by the time a caller inspects it. A stalled writer is abandoned rather
// than waited on.
func (t *liveTee) close() {
	close(t.chunks)
	timer := time.NewTimer(liveTeeFlushWait)
	defer timer.Stop()
	select {
	case <-t.done:
	case <-timer.C:
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
