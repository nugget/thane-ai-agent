package logging

import (
	"bufio"
	"io"
	"net"
	"net/http"
)

// AccessResponseWriter captures HTTP status code and bytes written while
// preserving the optional interfaces needed by streaming handlers.
type AccessResponseWriter struct {
	http.ResponseWriter
	statusCode int
	bytes      int64
}

// NewAccessResponseWriter wraps w and records status/byte counts.
func NewAccessResponseWriter(w http.ResponseWriter) *AccessResponseWriter {
	return &AccessResponseWriter{ResponseWriter: w}
}

// StatusCode returns the final response status, defaulting to 200 when
// the handler never called WriteHeader explicitly.
func (w *AccessResponseWriter) StatusCode() int {
	if w.statusCode == 0 {
		return http.StatusOK
	}
	return w.statusCode
}

// BytesWritten returns the number of response bytes written so far.
func (w *AccessResponseWriter) BytesWritten() int64 {
	return w.bytes
}

// WriteHeader records the status before delegating.
func (w *AccessResponseWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

// Write records bytes written and defaults the status to 200 when needed.
func (w *AccessResponseWriter) Write(p []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

// Flush preserves streaming support when the underlying writer supports it.
func (w *AccessResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		if w.statusCode == 0 {
			w.statusCode = http.StatusOK
		}
		flusher.Flush()
	}
}

// Hijack preserves websocket and raw-connection support when available.
func (w *AccessResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

// Push preserves HTTP/2 server push when the underlying writer supports it.
func (w *AccessResponseWriter) Push(target string, opts *http.PushOptions) error {
	pusher, ok := w.ResponseWriter.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, opts)
}

// ReadFrom preserves the optimized io.ReaderFrom path when available.
func (w *AccessResponseWriter) ReadFrom(src io.Reader) (int64, error) {
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		if w.statusCode == 0 {
			w.statusCode = http.StatusOK
		}
		n, err := rf.ReadFrom(src)
		w.bytes += n
		return n, err
	}
	return io.Copy(w, src)
}

// Unwrap returns the underlying [http.ResponseWriter]. This is the
// convention [http.NewResponseController] uses to walk through
// middleware wrappers to reach optional interfaces like
// [http.ResponseController.SetReadDeadline] and
// [http.ResponseController.SetWriteDeadline]. Without it, SSE and
// other streaming handlers behind this middleware cannot adjust their
// deadlines.
func (w *AccessResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
