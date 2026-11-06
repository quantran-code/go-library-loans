package main

import (
	"log"
	"net/http"
	"os"
)

// requestIDHeader carries the trace id of a request end to end.
const requestIDHeader = "X-Request-Id"

// requestLogger writes the per-request trace line. It is built over stdout so
// running the service prints the request_id lines on standard output; tests
// swap its output with SetOutput and restore it afterwards.
var requestLogger = log.New(os.Stdout, "", log.LstdFlags)

// statusRecorder remembers the status the wrapped handler wrote so the trace
// line can report it. A handler that never calls WriteHeader is recorded as 200.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status, then delegates to the embedded writer.
func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// withRequestTrace echoes the incoming request id and logs exactly one line
// describing the request. The id is taken from the request as-is: none is
// generated and none is replaced, so a request without the header logs and
// echoes an empty id.
func withRequestTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		w.Header().Set(requestIDHeader, id)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		requestLogger.Printf("request_id=%s method=%s path=%s status=%d", id, r.Method, r.URL.Path, rec.status)
	})
}
