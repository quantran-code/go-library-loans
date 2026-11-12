package main

import (
	"context"
	"log"
	"net/http"
)

// requestIDHeader carries the id minted by the public entry point so the log
// lines of one user action can be matched across the whole chain.
const requestIDHeader = "X-Request-Id"

// requestIDKey is the context key under which withRequestID stores the id.
type requestIDKey struct{}

// requestIDFrom returns the id carried by ctx, or "" when the request arrived
// without one.
func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// withRequestID stores the inbound id in the request context, echoes it on the
// response when it is set and logs one line per request. It never invents an
// id: a request that arrives without one stays empty and is logged as
// request_id=(none).
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id != "" {
			w.Header().Set(requestIDHeader, id)
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))

		logged := id
		if logged == "" {
			logged = "(none)"
		}
		log.Printf("loans method=%s path=%s status=%d request_id=%s", r.Method, r.URL.Path, rec.status, logged)
	})
}

// statusRecorder captures the status the handler writes so the request's log
// line can report it. It starts at 200 so a handler that only writes a body
// without an explicit WriteHeader is logged as 200.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the status the handler wrote and passes it on.
func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
