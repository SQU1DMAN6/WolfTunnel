//go:build linux

package audit

import (
	"bufio"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
)

// CaptureNativeStderr preserves terminal output from native libraries while
// recording safe categories, never their arbitrary text. FD redirection also
// captures C fprintf diagnostics that bypass Go and SDL logging callbacks.
func CaptureNativeStderr(logger *Logger) (func(), error) {
	saved, e := syscall.Dup(2)
	if e != nil {
		return nil, e
	}
	terminal := os.NewFile(uintptr(saved), "native-diagnostic-terminal")
	reader, writer, e := os.Pipe()
	if e != nil {
		terminal.Close()
		return nil, e
	}
	// ARM64 implements dup3 rather than the legacy dup2 syscall. Both supported
	// Linux architectures provide dup3; flags=0 keeps stderr open across exec.
	if e = syscall.Dup3(int(writer.Fd()), 2, 0); e != nil {
		reader.Close()
		writer.Close()
		terminal.Close()
		return nil, e
	}
	writer.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer reader.Close()
		input := bufio.NewReaderSize(reader, 16<<10)
		for {
			chunk, err := input.ReadSlice('\n')
			if len(chunk) > 0 {
				_, _ = terminal.Write(chunk)
				code, detail := nativeDiagnostic(string(chunk))
				if logger != nil {
					logger.Log(Entry{Component: "native", Event: "native_diagnostic", Code: code, Error: detail})
				}
			}
			if err != nil && err != bufio.ErrBufferFull {
				if err != io.EOF && logger != nil {
					logger.Log(Entry{Component: "native", Event: "native_diagnostic_capture_failed", Code: "NATIVE_LOG_READ_FAILED"})
				}
				return
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { _ = syscall.Dup3(saved, 2, 0); <-done; terminal.Close() }) }, nil
}
func nativeDiagnostic(message string) (string, string) {
	if strings.Contains(message, "libdecor") && strings.Contains(message, "failed") {
		return "NATIVE_DECORATION_WARNING", "An optional window-decoration plugin failed; the desktop may use another available backend."
	}
	return "NATIVE_RUNTIME_DIAGNOSTIC", "The native runtime emitted a diagnostic; raw text is omitted to protect credentials and inputs."
}
