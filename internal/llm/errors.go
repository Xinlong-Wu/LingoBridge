package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
)

// HTTPError is returned when an LLM provider responds with a non-success status.
type HTTPError struct {
	Label      string
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	label := e.Label
	if label == "" {
		label = "provider"
	}
	body := truncateStr(e.Body, 500)
	if body == "" {
		return fmt.Sprintf("%s HTTP %d", label, e.StatusCode)
	}
	return fmt.Sprintf("%s HTTP %d: %s", label, e.StatusCode, body)
}

func newHTTPError(label string, statusCode int, body []byte) *HTTPError {
	return &HTTPError{Label: label, StatusCode: statusCode, Body: string(body)}
}

// IsRetryableError reports whether an LLM provider error is likely transient.
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case 408, 409, 425, 429, 500, 502, 503, 504:
			return true
		default:
			return false
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}
	return isRetryableSyscallError(err)
}

func isRetryableSyscallError(err error) bool {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	var syscallErr *os.SyscallError
	if errors.As(err, &syscallErr) {
		err = syscallErr.Err
	}
	for _, target := range []error{
		syscall.ECONNRESET,
		syscall.ECONNREFUSED,
		syscall.ECONNABORTED,
		syscall.EPIPE,
		syscall.ETIMEDOUT,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
