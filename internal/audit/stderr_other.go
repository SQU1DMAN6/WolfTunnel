//go:build !linux

package audit

// Native FD capture is implemented on the validated Linux platform. Structured
// Go/connection errors are still logged on other client platforms.
func CaptureNativeStderr(logger *Logger) (func(), error) { return func() {}, nil }
