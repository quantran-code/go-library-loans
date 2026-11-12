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

// getPath sends a GET request to the router.
func getPath(t *testing.T, router http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// postReturn sends a return request to the router.
func postReturn(t *testing.T, router http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	return rec
}

// loanObject decodes a loan answer into a generic object so a test can tell an
// absent returnedAt key from one carrying a value.
func loanObject(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding loan %s: %v", rec.Body.String(), err)
	}
	return body
}

// recordLoan puts one active loan for book 1 straight into the store, for the
// tests that exercise returning without borrowing first.
func recordLoan(t *testing.T, store *MemoryLoanStore) {
	t.Helper()

	if _, err := store.Record(1, time.Now()); err != nil {
		t.Fatalf("recording loan: %v", err)
	}
}

func TestReturnPutsTheCopyBackAndStampsReturnedAt(t *testing.T) {
	borrowStub := newStubCatalog(t, http.StatusOK, availableBookJSON, http.StatusOK)
	store := NewMemoryLoanStore()
	borrowRouter := newRouter(store, NewCatalogClient(borrowStub.server.URL))

	if rec := postLoan(t, borrowRouter, `{"bookId":1}`); rec.Code != http.StatusCreated {
		t.Fatalf("borrow status %d, body %s", rec.Code, rec.Body.String())
	}

	stub := newStubCatalog(t, http.StatusOK, bookOnLoanJSON, http.StatusOK)
	router := newRouter(store, NewCatalogClient(stub.server.URL))

	rec := postReturn(t, router, "/loans/1/return")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	body := loanObject(t, rec)
	if id, ok := body["id"].(float64); !ok || id != 1 {
		t.Fatalf("loan id %v, want 1", body["id"])
	}
	stamp, ok := body["returnedAt"].(string)
	if !ok {
		t.Fatalf("returned loan %v has no returnedAt", body)
	}
	if !strings.HasSuffix(stamp, "Z") {
		t.Fatalf("returnedAt %q is not UTC", stamp)
	}
	if _, err := time.Parse(time.RFC3339, stamp); err != nil {
		t.Fatalf("returnedAt %q does not parse as RFC 3339: %v", stamp, err)
	}

	patches := stub.patches()
	if len(patches) != 1 {
		t.Fatalf("catalogue saw %d availability changes, want 1: %+v", len(patches), patches)
	}
	if patches[0].path != "/books/1/availability" {
		t.Fatalf("availability path %q, want /books/1/availability", patches[0].path)
	}
	if available, ok := patches[0].body["available"]; !ok || !available {
		t.Fatalf("patch body %v, want available true", patches[0].body)
	}

	fetched := getPath(t, router, "/loans/1")
	if fetched.Code != http.StatusOK {
		t.Fatalf("get status %d, body %s", fetched.Code, fetched.Body.String())
	}
	stored := loanObject(t, fetched)
	if got, ok := stored["returnedAt"].(string); !ok || got != stamp {
		t.Fatalf("stored returnedAt %v, want %q", stored["returnedAt"], stamp)
	}

	listed := getPath(t, router, "/loans")
	if listed.Code != http.StatusOK {
		t.Fatalf("list status %d, body %s", listed.Code, listed.Body.String())
	}
	var loans []map[string]any
	if err := json.Unmarshal(listed.Body.Bytes(), &loans); err != nil {
		t.Fatalf("decoding loans %s: %v", listed.Body.String(), err)
	}
	if len(loans) != 1 {
		t.Fatalf("list answers %d loans, want 1: %s", len(loans), listed.Body.String())
	}
	if got, ok := loans[0]["returnedAt"].(string); !ok || got != stamp {
		t.Fatalf("listed returnedAt %v, want %q", loans[0]["returnedAt"], stamp)
	}
}

func TestReturningTwiceAnswers409(t *testing.T) {
	store := NewMemoryLoanStore()
	recordLoan(t, store)
	stub := newStubCatalog(t, http.StatusOK, bookOnLoanJSON, http.StatusOK)
	router := newRouter(store, NewCatalogClient(stub.server.URL))

	first := postReturn(t, router, "/loans/1/return")
	if first.Code != http.StatusOK {
		t.Fatalf("first return status %d, body %s", first.Code, first.Body.String())
	}

	second := postReturn(t, router, "/loans/1/return")
	if second.Code != http.StatusConflict {
		t.Fatalf("second return status %d, body %s", second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), `"error":"loan already returned"`) {
		t.Fatalf("unexpected body: %s", second.Body.String())
	}

	if patches := stub.patches(); len(patches) != 1 {
		t.Fatalf("catalogue saw %d availability changes, want 1: %+v", len(patches), patches)
	}
}

func TestReturnForAnUnknownLoanAnswers404(t *testing.T) {
	stub := newStubCatalog(t, http.StatusOK, bookOnLoanJSON, http.StatusOK)
	router := newRouter(NewMemoryLoanStore(), NewCatalogClient(stub.server.URL))

	rec := postReturn(t, router, "/loans/999/return")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"loan not found"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if requests := stub.seen(); len(requests) != 0 {
		t.Fatalf("catalogue saw %+v, want no requests", requests)
	}
}

func TestReturnForAnInvalidLoanIdAnswers400(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{name: "not a number", path: "/loans/abc/return"},
		{name: "zero", path: "/loans/0/return"},
		{name: "negative", path: "/loans/-3/return"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemoryLoanStore()
			recordLoan(t, store)
			stub := newStubCatalog(t, http.StatusOK, bookOnLoanJSON, http.StatusOK)
			router := newRouter(store, NewCatalogClient(stub.server.URL))

			rec := postReturn(t, router, tc.path)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"error":"invalid loan id"`) {
				t.Fatalf("unexpected body: %s", rec.Body.String())
			}
			if requests := stub.seen(); len(requests) != 0 {
				t.Fatalf("catalogue saw %+v, want no requests", requests)
			}
		})
	}
}

func TestReturningABookThatIsAlreadyAvailableAnswers409AndChangesNothing(t *testing.T) {
	store := NewMemoryLoanStore()
	recordLoan(t, store)
	stub := newStubCatalog(t, http.StatusOK, availableBookJSON, http.StatusOK)
	router := newRouter(store, NewCatalogClient(stub.server.URL))

	rec := postReturn(t, router, "/loans/1/return")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"book not on loan"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if patches := stub.patches(); len(patches) != 0 {
		t.Fatalf("catalogue saw %+v, want no availability change", patches)
	}

	fetched := getPath(t, router, "/loans/1")
	if fetched.Code != http.StatusOK {
		t.Fatalf("get status %d, body %s", fetched.Code, fetched.Body.String())
	}
	if _, ok := loanObject(t, fetched)["returnedAt"]; ok {
		t.Fatalf("refused return stamped the loan: %s", fetched.Body.String())
	}
}

func TestReturnWhenTheCatalogueFailsAnswers502AndLeavesTheLoanOpen(t *testing.T) {
	store := NewMemoryLoanStore()
	recordLoan(t, store)

	failing := newStubCatalog(t, http.StatusOK, bookOnLoanJSON, http.StatusInternalServerError)
	router := newRouter(store, NewCatalogClient(failing.server.URL))

	rec := postReturn(t, router, "/loans/1/return")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"catalog unavailable"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	if patches := failing.patches(); len(patches) != 1 {
		t.Fatalf("catalogue saw %d availability changes, want 1: %+v", len(patches), patches)
	}

	fetched := getPath(t, router, "/loans/1")
	if fetched.Code != http.StatusOK {
		t.Fatalf("get status %d, body %s", fetched.Code, fetched.Body.String())
	}
	if _, ok := loanObject(t, fetched)["returnedAt"]; ok {
		t.Fatalf("failed return stamped the loan: %s", fetched.Body.String())
	}

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	closedRouter := newRouter(store, NewCatalogClient(closedURL))
	closedRec := postReturn(t, closedRouter, "/loans/1/return")
	if closedRec.Code != http.StatusBadGateway {
		t.Fatalf("status %d for an unreachable catalogue, body %s", closedRec.Code, closedRec.Body.String())
	}
	if !strings.Contains(closedRec.Body.String(), `"error":"catalog unavailable"`) {
		t.Fatalf("unexpected body: %s", closedRec.Body.String())
	}

	closedFetched := getPath(t, closedRouter, "/loans/1")
	if closedFetched.Code != http.StatusOK {
		t.Fatalf("get status %d, body %s", closedFetched.Code, closedFetched.Body.String())
	}
	if _, ok := loanObject(t, closedFetched)["returnedAt"]; ok {
		t.Fatalf("failed return stamped the loan: %s", closedFetched.Body.String())
	}
}

func TestListLoansReturnsNewestFirstAndEmptyListIsNotNull(t *testing.T) {
	emptyStub := newStubCatalog(t, http.StatusOK, availableBookJSON, http.StatusOK)
	emptyRouter := newRouter(NewMemoryLoanStore(), NewCatalogClient(emptyStub.server.URL))

	empty := getPath(t, emptyRouter, "/loans")
	if empty.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", empty.Code, empty.Body.String())
	}
	if body := strings.TrimSpace(empty.Body.String()); body != "[]" {
		t.Fatalf("empty list answers %q, want []", body)
	}

	stub := newStubCatalog(t, http.StatusOK, availableBookJSON, http.StatusOK)
	router := newRouter(NewMemoryLoanStore(), NewCatalogClient(stub.server.URL))

	if rec := postLoan(t, router, `{"bookId":1}`); rec.Code != http.StatusCreated {
		t.Fatalf("first borrow status %d, body %s", rec.Code, rec.Body.String())
	}
	if rec := postLoan(t, router, `{"bookId":1}`); rec.Code != http.StatusCreated {
		t.Fatalf("second borrow status %d, body %s", rec.Code, rec.Body.String())
	}

	rec := getPath(t, router, "/loans")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	var listed []struct {
		ID         int     `json:"id"`
		ReturnedAt *string `json:"returnedAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decoding loans %s: %v", rec.Body.String(), err)
	}
	if len(listed) != 2 {
		t.Fatalf("list answers %d loans, want 2: %s", len(listed), rec.Body.String())
	}
	if listed[0].ID != 2 {
		t.Fatalf("first listed loan id %d, want 2", listed[0].ID)
	}
	if listed[1].ID != 1 {
		t.Fatalf("second listed loan id %d, want 1", listed[1].ID)
	}
	for _, loan := range listed {
		if loan.ReturnedAt != nil {
			t.Fatalf("active loan %d carries returnedAt: %s", loan.ID, rec.Body.String())
		}
	}
}

func TestGetLoanAnswers200And404(t *testing.T) {
	stub := newStubCatalog(t, http.StatusOK, availableBookJSON, http.StatusOK)
	router := newRouter(NewMemoryLoanStore(), NewCatalogClient(stub.server.URL))

	if rec := postLoan(t, router, `{"bookId":1}`); rec.Code != http.StatusCreated {
		t.Fatalf("borrow status %d, body %s", rec.Code, rec.Body.String())
	}

	rec := getPath(t, router, "/loans/1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if loan := decodeBorrowing(t, rec); loan.ID != 1 || loan.BookID != 1 {
		t.Fatalf("loan %+v, want id 1 of book 1", loan)
	}
	if _, ok := loanObject(t, rec)["returnedAt"]; ok {
		t.Fatalf("active loan carries returnedAt: %s", rec.Body.String())
	}

	missing := getPath(t, router, "/loans/999")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("status %d, body %s", missing.Code, missing.Body.String())
	}
	if !strings.Contains(missing.Body.String(), `"error":"loan not found"`) {
		t.Fatalf("unexpected body: %s", missing.Body.String())
	}

	for _, path := range []string{"/loans/abc", "/loans/0"} {
		invalid := getPath(t, router, path)
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, body %s", path, invalid.Code, invalid.Body.String())
		}
		if !strings.Contains(invalid.Body.String(), `"error":"invalid loan id"`) {
			t.Fatalf("%s: unexpected body: %s", path, invalid.Body.String())
		}
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

func (s *recordingStore) List() []Loan {
	s.mu.Lock()
	defer s.mu.Unlock()

	loans := make([]Loan, 0, len(s.loans))
	for i := len(s.loans) - 1; i >= 0; i-- {
		loans = append(loans, s.loans[i])
	}

	return loans
}

func (s *recordingStore) Get(id int) (Loan, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, loan := range s.loans {
		if loan.ID == id {
			return loan, true
		}
	}

	return Loan{}, false
}

func (s *recordingStore) MarkReturned(id int, returnedAt time.Time) (Loan, error) {
	returned := returnedAt.UTC().Truncate(time.Second)

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, loan := range s.loans {
		if loan.ID != id {
			continue
		}
		if loan.ReturnedAt != nil {
			return Loan{}, ErrLoanAlreadyReturned
		}

		s.loans[i].ReturnedAt = &returned

		return s.loans[i], nil
	}

	return Loan{}, ErrLoanNotFound
}

// failingLoanStore always fails, so the restore path can be exercised.
type failingLoanStore struct{}

func (failingLoanStore) Record(int, time.Time) (Loan, error) {
	return Loan{}, errors.New("recording loan failed")
}

func (failingLoanStore) List() []Loan {
	return []Loan{}
}

func (failingLoanStore) Get(int) (Loan, bool) {
	return Loan{}, false
}

func (failingLoanStore) MarkReturned(int, time.Time) (Loan, error) {
	return Loan{}, errors.New("recording return failed")
}
