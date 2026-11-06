package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// captureTraceLog redirects the trace logger to a buffer for the duration of a
// test and restores it afterwards.
func captureTraceLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	requestLogger.SetOutput(&buf)
	t.Cleanup(func() {
		requestLogger.SetOutput(os.Stdout)
	})
	return &buf
}

// singleTraceLine returns the one trace line written to logs, failing the test
// when none or more than one was written.
func singleTraceLine(t *testing.T, logs *bytes.Buffer) string {
	t.Helper()
	out := strings.TrimRight(logs.String(), "\n")
	if out == "" {
		t.Fatalf("no trace log line was written")
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 1 {
		t.Fatalf("wrote %d trace lines, want 1: %q", len(lines), out)
	}
	return lines[0]
}

// traceCatalog answers the catalogue calls a borrow or a return makes.
func traceCatalog(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/books/"):
			bookID := strings.TrimPrefix(r.URL.Path, "/books/")
			fmt.Fprintf(w, `{"id":%s,"title":"Book","available":true}`, bookID)
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/availability"):
			fmt.Fprint(w, `{"available":true}`)
		default:
			t.Errorf("unexpected catalog request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

// seedOutstandingLoan puts one loan in the store for the routes that need an id.
func seedOutstandingLoan() {
	borrowedAt := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	loans = []Loan{{ID: 1, BookID: 7, BorrowedAt: borrowedAt, DueAt: borrowedAt.Add(loanPeriod)}}
	nextLoanID = 2
}

// seedReturnedLoan puts one already returned loan in the store.
func seedReturnedLoan() {
	seedOutstandingLoan()
	returnedAt := time.Date(2024, 5, 20, 9, 0, 0, 0, time.UTC)
	loans[0].ReturnedAt = &returnedAt
}

func TestLoanRoutesEchoAndLogRequestID(t *testing.T) {
	const requestID = "abc-123"

	catalog := traceCatalog(t)

	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		seed       func()
		wantStatus int
	}{
		{
			name:       "borrow",
			method:     http.MethodPost,
			path:       "/loans",
			body:       `{"bookId":1}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "list",
			method:     http.MethodGet,
			path:       "/loans",
			wantStatus: http.StatusOK,
		},
		{
			name:       "get",
			method:     http.MethodGet,
			path:       "/loans/1",
			seed:       seedOutstandingLoan,
			wantStatus: http.StatusOK,
		},
		{
			name:       "return",
			method:     http.MethodPost,
			path:       "/loans/1/return",
			seed:       seedOutstandingLoan,
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetLoanStore(t)
			if tc.seed != nil {
				tc.seed()
			}
			t.Setenv("CATALOG_URL", catalog.URL)
			logs := captureTraceLog(t)

			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set(requestIDHeader, requestID)

			rec := httptest.NewRecorder()
			NewRouter().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := rec.Header().Get(requestIDHeader); got != requestID {
				t.Errorf("%s = %q, want %q", requestIDHeader, got, requestID)
			}

			line := singleTraceLine(t, logs)
			want := fmt.Sprintf("request_id=%s method=%s path=%s status=%d", requestID, tc.method, tc.path, tc.wantStatus)
			if !strings.Contains(line, want) {
				t.Errorf("log line %q, want it to contain %q", line, want)
			}
		})
	}
}

func TestLoanRoutesDoNotInventRequestID(t *testing.T) {
	catalog := traceCatalog(t)

	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		seed       func()
		wantStatus int
	}{
		{
			name:       "borrow",
			method:     http.MethodPost,
			path:       "/loans",
			body:       `{"bookId":1}`,
			wantStatus: http.StatusCreated,
		},
		{
			name:       "list",
			method:     http.MethodGet,
			path:       "/loans",
			wantStatus: http.StatusOK,
		},
		{
			name:       "get",
			method:     http.MethodGet,
			path:       "/loans/1",
			seed:       seedOutstandingLoan,
			wantStatus: http.StatusOK,
		},
		{
			name:       "return",
			method:     http.MethodPost,
			path:       "/loans/1/return",
			seed:       seedOutstandingLoan,
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetLoanStore(t)
			if tc.seed != nil {
				tc.seed()
			}
			t.Setenv("CATALOG_URL", catalog.URL)
			logs := captureTraceLog(t)

			rec := httptest.NewRecorder()
			NewRouter().ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := rec.Header().Get(requestIDHeader); got != "" {
				t.Errorf("%s = %q, want no generated id", requestIDHeader, got)
			}

			line := singleTraceLine(t, logs)
			if !strings.Contains(line, "request_id= method=") {
				t.Errorf("log line %q, want an empty request_id field", line)
			}
		})
	}
}

func TestTraceLogsTheRealStatus(t *testing.T) {
	const requestID = "abc-123"

	catalog := traceCatalog(t)

	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		seed       func()
		wantStatus int
	}{
		{
			name:       "invalid book id",
			method:     http.MethodPost,
			path:       "/loans",
			body:       `{"bookId":0}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown loan",
			method:     http.MethodGet,
			path:       "/loans/99",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "already returned",
			method:     http.MethodPost,
			path:       "/loans/1/return",
			seed:       seedReturnedLoan,
			wantStatus: http.StatusConflict,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetLoanStore(t)
			if tc.seed != nil {
				tc.seed()
			}
			t.Setenv("CATALOG_URL", catalog.URL)
			logs := captureTraceLog(t)

			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set(requestIDHeader, requestID)

			rec := httptest.NewRecorder()
			NewRouter().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := rec.Header().Get(requestIDHeader); got != requestID {
				t.Errorf("%s = %q, want %q", requestIDHeader, got, requestID)
			}

			line := singleTraceLine(t, logs)
			wantStatusField := fmt.Sprintf("status=%d", tc.wantStatus)
			if !strings.Contains(line, wantStatusField) {
				t.Errorf("log line %q, want it to contain %q", line, wantStatusField)
			}
		})
	}
}
