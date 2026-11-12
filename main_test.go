package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	availableBookJSON = `{"id":1,"title":"Dune","author":"Frank Herbert","available":true}`
	bookOnLoanJSON    = `{"id":1,"title":"Dune","author":"Frank Herbert","available":false}`
)

func TestHealthReportsTheServiceIsUp(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

// catalogRequest is one call the stub catalogue saw.
type catalogRequest struct {
	method string
	path   string
	body   map[string]bool
}

// stubCatalog answers GET /books/{id} with a fixed book and records every
// request it receives.
type stubCatalog struct {
	server      *httptest.Server
	mu          sync.Mutex
	requests    []catalogRequest
	getStatus   int
	book        string
	patchStatus int
}

func newStubCatalog(t *testing.T, getStatus int, book string, patchStatus int) *stubCatalog {
	t.Helper()

	stub := &stubCatalog{getStatus: getStatus, book: book, patchStatus: patchStatus}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen := catalogRequest{method: r.Method, path: r.URL.Path}
		if r.Method == http.MethodPatch {
			var body map[string]bool
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding patch body: %v", err)
			}
			seen.body = body
		}

		stub.mu.Lock()
		stub.requests = append(stub.requests, seen)
		stub.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(stub.getStatus)
			if stub.getStatus == http.StatusOK {
				_, _ = w.Write([]byte(stub.book))
			}
		case http.MethodPatch:
			w.WriteHeader(stub.patchStatus)
			if stub.patchStatus == http.StatusOK {
				_, _ = w.Write([]byte(bookOnLoanJSON))
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(stub.server.Close)

	return stub
}

func (s *stubCatalog) seen() []catalogRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]catalogRequest(nil), s.requests...)
}

func (s *stubCatalog) patches() []catalogRequest {
	var patches []catalogRequest
	for _, request := range s.seen() {
		if request.method == http.MethodPatch {
			patches = append(patches, request)
		}
	}
	return patches
}

// postLoan sends a borrow request to the router.
func postLoan(t *testing.T, router http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/loans", strings.NewReader(body)))
	return rec
}

// borrowing mirrors the 201 body so the timestamps stay strings and can be
// checked against time.RFC3339.
type borrowing struct {
	ID         int    `json:"id"`
	BookID     int    `json:"bookId"`
	BorrowedAt string `json:"borrowedAt"`
	DueAt      string `json:"dueAt"`
}

func decodeBorrowing(t *testing.T, rec *httptest.ResponseRecorder) borrowing {
	t.Helper()

	var body borrowing
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding loan %s: %v", rec.Body.String(), err)
	}
	return body
}

func TestBorrowTakesTheCopyAndRecordsTheLoan(t *testing.T) {
	stub := newStubCatalog(t, http.StatusOK, availableBookJSON, http.StatusOK)
	router := newRouter(NewMemoryLoanStore(), NewCatalogClient(stub.server.URL))

	rec := postLoan(t, router, `{"bookId":1}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	loan := decodeBorrowing(t, rec)
	if loan.ID != 1 {
		t.Fatalf("loan id %d, want 1", loan.ID)
	}
	if loan.BookID != 1 {
		t.Fatalf("loan book id %d, want 1", loan.BookID)
	}

	borrowedAt, err := time.Parse(time.RFC3339, loan.BorrowedAt)
	if err != nil {
		t.Fatalf("borrowedAt %q does not parse as RFC 3339: %v", loan.BorrowedAt, err)
	}
	dueAt, err := time.Parse(time.RFC3339, loan.DueAt)
	if err != nil {
		t.Fatalf("dueAt %q does not parse as RFC 3339: %v", loan.DueAt, err)
	}
	if got := dueAt.Sub(borrowedAt); got != 14*24*time.Hour {
		t.Fatalf("dueAt - borrowedAt = %v, want %v", got, 14*24*time.Hour)
	}
	if strings.Contains(loan.BorrowedAt, ".") {
		t.Fatalf("borrowedAt %q carries a fractional second", loan.BorrowedAt)
	}
	if !strings.HasSuffix(loan.BorrowedAt, "Z") || !strings.HasSuffix(loan.DueAt, "Z") {
		t.Fatalf("timestamps %q and %q are not UTC", loan.BorrowedAt, loan.DueAt)
	}

	requests := stub.seen()
	if len(requests) != 2 {
		t.Fatalf("catalogue saw %d requests, want 2: %+v", len(requests), requests)
	}
	if requests[0].method != http.MethodGet || requests[0].path != "/books/1" {
		t.Fatalf("first request %+v, want GET /books/1", requests[0])
	}
	if requests[1].method != http.MethodPatch || requests[1].path != "/books/1/availability" {
		t.Fatalf("second request %+v, want PATCH /books/1/availability", requests[1])
	}
	if available, ok := requests[1].body["available"]; !ok || available {
		t.Fatalf("patch body %v, want available false", requests[1].body)
	}
}

func TestBorrowRecordsTheNthLoanWithIdN(t *testing.T) {
	stub := newStubCatalog(t, http.StatusOK, availableBookJSON, http.StatusOK)
	router := newRouter(NewMemoryLoanStore(), NewCatalogClient(stub.server.URL))

	first := postLoan(t, router, `{"bookId":1}`)
	second := postLoan(t, router, `{"bookId":1}`)

	if first.Code != http.StatusCreated || second.Code != http.StatusCreated {
		t.Fatalf("statuses %d and %d, want 201 both", first.Code, second.Code)
	}
	if id := decodeBorrowing(t, first).ID; id != 1 {
		t.Fatalf("first loan id %d, want 1", id)
	}
	if id := decodeBorrowing(t, second).ID; id != 2 {
		t.Fatalf("second loan id %d, want 2", id)
	}
}

func TestUnknownBookAnswers404(t *testing.T) {
	stub := newStubCatalog(t, http.StatusNotFound, "", http.StatusOK)
	router := newRouter(NewMemoryLoanStore(), NewCatalogClient(stub.server.URL))

	rec := postLoan(t, router, `{"bookId":999}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"book not found"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if patches := stub.patches(); len(patches) != 0 {
		t.Fatalf("catalogue saw %+v, want no availability change", patches)
	}
}

func TestBookAlreadyOnLoanAnswers409(t *testing.T) {
	stub := newStubCatalog(t, http.StatusOK, bookOnLoanJSON, http.StatusOK)
	router := newRouter(NewMemoryLoanStore(), NewCatalogClient(stub.server.URL))

	rec := postLoan(t, router, `{"bookId":1}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"book already on loan"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if patches := stub.patches(); len(patches) != 0 {
		t.Fatalf("catalogue saw %+v, want no availability change", patches)
	}
}

func TestInvalidBookIdAnswers400(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "missing", body: `{}`},
		{name: "not a number", body: `{"bookId":"abc"}`},
		{name: "zero", body: `{"bookId":0}`},
		{name: "negative", body: `{"bookId":-3}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newStubCatalog(t, http.StatusOK, availableBookJSON, http.StatusOK)
			store := &recordingStore{}
			router := newRouter(store, NewCatalogClient(stub.server.URL))

			rec := postLoan(t, router, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"error":"invalid book id"`) {
				t.Fatalf("unexpected body: %s", rec.Body.String())
			}
			if requests := stub.seen(); len(requests) != 0 {
				t.Fatalf("catalogue saw %+v, want no requests", requests)
			}
			if store.count() != 0 {
				t.Fatalf("recorded %d loans, want none", store.count())
			}
		})
	}
}

func TestMalformedBodyAnswers400(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "truncated", body: `{"bookId":`},
		{name: "trailing value", body: `{"bookId":1} {"bookId":2}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newStubCatalog(t, http.StatusOK, availableBookJSON, http.StatusOK)
			store := &recordingStore{}
			router := newRouter(store, NewCatalogClient(stub.server.URL))

			rec := postLoan(t, router, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"error":"invalid request body"`) {
				t.Fatalf("unexpected body: %s", rec.Body.String())
			}
			if requests := stub.seen(); len(requests) != 0 {
				t.Fatalf("catalogue saw %+v, want no requests", requests)
			}
			if store.count() != 0 {
				t.Fatalf("recorded %d loans, want none", store.count())
			}
		})
	}
}

func TestCatalogFailureAnswers502(t *testing.T) {
	stub := newStubCatalog(t, http.StatusInternalServerError, "", http.StatusOK)
	router := newRouter(NewMemoryLoanStore(), NewCatalogClient(stub.server.URL))

	rec := postLoan(t, router, `{"bookId":1}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"catalog unavailable"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	closedRec := postLoan(t, newRouter(NewMemoryLoanStore(), NewCatalogClient(closedURL)), `{"bookId":1}`)
	if closedRec.Code != http.StatusBadGateway {
		t.Fatalf("status %d for an unreachable catalogue, body %s", closedRec.Code, closedRec.Body.String())
	}
	if !strings.Contains(closedRec.Body.String(), `"error":"catalog unavailable"`) {
		t.Fatalf("unexpected body: %s", closedRec.Body.String())
	}
}

func TestAvailabilityFailureAnswers502AndRecordsNothing(t *testing.T) {
	stub := newStubCatalog(t, http.StatusOK, availableBookJSON, http.StatusInternalServerError)
	store := &recordingStore{}
	router := newRouter(store, NewCatalogClient(stub.server.URL))

	rec := postLoan(t, router, `{"bookId":1}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"catalog unavailable"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if store.count() != 0 {
		t.Fatalf("recorded %d loans, want none", store.count())
	}
}

func TestRecordingFailureRestoresTheCopy(t *testing.T) {
	stub := newStubCatalog(t, http.StatusOK, availableBookJSON, http.StatusOK)
	router := newRouter(failingLoanStore{}, NewCatalogClient(stub.server.URL))

	rec := postLoan(t, router, `{"bookId":1}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response %s is not JSON: %v", rec.Body.String(), err)
	}

	patches := stub.patches()
	if len(patches) != 2 {
		t.Fatalf("catalogue saw %d availability changes, want 2: %+v", len(patches), patches)
	}
	if patches[0].path != "/books/1/availability" || patches[1].path != "/books/1/availability" {
		t.Fatalf("availability changes %+v, want /books/1/availability both", patches)
	}
	if available, ok := patches[0].body["available"]; !ok || available {
		t.Fatalf("first patch body %v, want available false", patches[0].body)
	}
	if available, ok := patches[1].body["available"]; !ok || !available {
		t.Fatalf("second patch body %v, want available true", patches[1].body)
	}
}

// recordingStore counts the loans the handler records so a test can prove
// nothing was recorded.
type recordingStore struct {
	mu    sync.Mutex
	loans []Loan
}

func (s *recordingStore) Record(bookID int, borrowedAt time.Time) (Loan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	loan := Loan{
		ID:         len(s.loans) + 1,
		BookID:     bookID,
		BorrowedAt: borrowedAt.UTC().Truncate(time.Second),
		DueAt:      borrowedAt.UTC().Truncate(time.Second).AddDate(0, 0, 14),
	}
	s.loans = append(s.loans, loan)

	return loan, nil
}

func (s *recordingStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.loans)
}

// failingLoanStore always fails, so the restore path can be exercised.
type failingLoanStore struct{}

func (failingLoanStore) Record(int, time.Time) (Loan, error) {
	return Loan{}, errors.New("recording loan failed")
}
