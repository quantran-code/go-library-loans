package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// resetLoanStore empties the in-memory loan store before and after a test so
// cases do not see each other's borrows.
func resetLoanStore(t *testing.T) {
	t.Helper()
	loans = nil
	nextLoanID = 1
	t.Cleanup(func() {
		loans = nil
		nextLoanID = 1
	})
}

// recordingCatalog wraps a fake catalogue and counts the requests it receives.
func recordingCatalog(hits *int, mu *sync.Mutex, next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		(*hits)++
		mu.Unlock()
		next.ServeHTTP(w, r)
	}
}

// closedPortURL returns a URL nothing is listening on.
func closedPortURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("closing listener: %v", err)
	}
	return "http://" + addr
}

func TestBorrowRecordsLoanAndMarksBookUnavailable(t *testing.T) {
	resetLoanStore(t)

	var mu sync.Mutex
	var calls []string
	var patchedBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/books/1":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":1,"title":"Dune","available":true}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/books/1/availability":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("reading patch body: %v", err)
			}
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("patch Content-Type = %q, want application/json", got)
			}
			mu.Lock()
			patchedBody = string(body)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":1,"title":"Dune","available":false}`)
		default:
			t.Errorf("unexpected catalog request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("CATALOG_URL", srv.URL)

	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/loans", strings.NewReader(`{"bookId":1}`)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var loan Loan
	if err := json.Unmarshal(rec.Body.Bytes(), &loan); err != nil {
		t.Fatalf("decoding loan: %v (body %s)", err, rec.Body.String())
	}
	if loan.ID == 0 {
		t.Errorf("loan id = 0, want a recorded id")
	}
	if loan.BookID != 1 {
		t.Errorf("bookId = %d, want 1", loan.BookID)
	}
	if loan.BorrowedAt.IsZero() {
		t.Errorf("borrowedAt is zero")
	}
	if got, want := loan.DueAt.Sub(loan.BorrowedAt), time.Duration(336)*time.Hour; got != want {
		t.Errorf("dueAt - borrowedAt = %v, want %v", got, want)
	}
	if len(loans) != 1 {
		t.Fatalf("recorded %d loans, want 1", len(loans))
	}

	mu.Lock()
	gotCalls := append([]string(nil), calls...)
	gotBody := patchedBody
	mu.Unlock()

	wantCalls := []string{"GET /books/1", "PATCH /books/1/availability"}
	if len(gotCalls) != len(wantCalls) {
		t.Fatalf("catalog calls = %v, want %v", gotCalls, wantCalls)
	}
	for i := range wantCalls {
		if gotCalls[i] != wantCalls[i] {
			t.Fatalf("catalog calls = %v, want %v", gotCalls, wantCalls)
		}
	}

	var patchPayload struct {
		Available *bool `json:"available"`
	}
	if err := json.Unmarshal([]byte(gotBody), &patchPayload); err != nil {
		t.Fatalf("decoding patch body %q: %v", gotBody, err)
	}
	if patchPayload.Available == nil || *patchPayload.Available {
		t.Fatalf("patch body = %q, want available false", gotBody)
	}
}

func TestBorrowRejectsUnavailableBook(t *testing.T) {
	resetLoanStore(t)

	var mu sync.Mutex
	var patches int
	var gets int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		switch {
		case r.Method == http.MethodGet:
			gets++
		case r.Method == http.MethodPatch:
			patches++
		}
		mu.Unlock()

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/books/2":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":2,"title":"Emma","available":false}`)
		case r.Method == http.MethodPatch:
			t.Errorf("unexpected availability patch for book already on loan")
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected catalog request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("CATALOG_URL", srv.URL)

	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/loans", strings.NewReader(`{"bookId":2}`)))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"book already on loan"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}

	mu.Lock()
	gotPatches := patches
	gotGets := gets
	mu.Unlock()

	if gotGets != 1 {
		t.Errorf("catalog saw %d GETs, want 1", gotGets)
	}
	if gotPatches != 0 {
		t.Errorf("catalog saw %d availability patches, want 0", gotPatches)
	}
	if len(loans) != 0 {
		t.Errorf("recorded %d loans, want 0", len(loans))
	}
}

func TestBorrowErrorCases(t *testing.T) {
	notFoundCatalog := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"book not found"}`)
	})
	availableCatalog := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":1,"title":"Dune","available":true}`)
	})
	failingPatchCatalog := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":1,"title":"Dune","available":true}`)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})

	cases := []struct {
		name            string
		body            string
		catalog         http.Handler
		wantCatalogHits int
		wantStatus      int
		wantError       string
	}{
		{
			name:            "zero book id",
			body:            `{"bookId":0}`,
			catalog:         availableCatalog,
			wantCatalogHits: 0,
			wantStatus:      http.StatusBadRequest,
			wantError:       "invalid book id",
		},
		{
			name:            "negative book id",
			body:            `{"bookId":-1}`,
			catalog:         availableCatalog,
			wantCatalogHits: 0,
			wantStatus:      http.StatusBadRequest,
			wantError:       "invalid book id",
		},
		{
			name:            "missing book id",
			body:            `{}`,
			catalog:         availableCatalog,
			wantCatalogHits: 0,
			wantStatus:      http.StatusBadRequest,
			wantError:       "invalid book id",
		},
		{
			name:            "non-json body",
			body:            `not json`,
			catalog:         availableCatalog,
			wantCatalogHits: 0,
			wantStatus:      http.StatusBadRequest,
			wantError:       "invalid book id",
		},
		{
			name:            "unknown book",
			body:            `{"bookId":99}`,
			catalog:         notFoundCatalog,
			wantCatalogHits: 1,
			wantStatus:      http.StatusNotFound,
			wantError:       "book not found",
		},
		{
			name:            "unreachable catalog",
			body:            `{"bookId":1}`,
			catalog:         nil,
			wantCatalogHits: 0,
			wantStatus:      http.StatusBadGateway,
			wantError:       "catalog unavailable",
		},
		{
			name:            "failed availability patch",
			body:            `{"bookId":1}`,
			catalog:         failingPatchCatalog,
			wantCatalogHits: 2,
			wantStatus:      http.StatusBadGateway,
			wantError:       "catalog unavailable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetLoanStore(t)

			var mu sync.Mutex
			var hits int
			catalogURL := closedPortURL(t)
			if tc.catalog != nil {
				srv := httptest.NewServer(recordingCatalog(&hits, &mu, tc.catalog))
				defer srv.Close()
				catalogURL = srv.URL
			}
			t.Setenv("CATALOG_URL", catalogURL)

			rec := httptest.NewRecorder()
			NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/loans", strings.NewReader(tc.body)))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"error":"`+tc.wantError+`"`) {
				t.Fatalf("body %s, want error %q", rec.Body.String(), tc.wantError)
			}

			mu.Lock()
			gotHits := hits
			mu.Unlock()
			if gotHits != tc.wantCatalogHits {
				t.Errorf("catalog received %d requests, want %d", gotHits, tc.wantCatalogHits)
			}
			if len(loans) != 0 {
				t.Errorf("recorded %d loans, want 0", len(loans))
			}
		})
	}
}

func TestListLoansAndGetLoan(t *testing.T) {
	resetLoanStore(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/books/"):
			w.Header().Set("Content-Type", "application/json")
			bookID := strings.TrimPrefix(r.URL.Path, "/books/")
			fmt.Fprintf(w, `{"id":%s,"title":"Book","available":true}`, bookID)
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/availability"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"available":false}`)
		default:
			t.Errorf("unexpected catalog request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("CATALOG_URL", srv.URL)

	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/loans", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("GET /loans body = %q, want []", got)
	}

	for _, bookID := range []int{1, 2} {
		rec := httptest.NewRecorder()
		body := fmt.Sprintf(`{"bookId":%d}`, bookID)
		NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/loans", strings.NewReader(body)))
		if rec.Code != http.StatusCreated {
			t.Fatalf("borrowing book %d: status %d, body %s", bookID, rec.Code, rec.Body.String())
		}
	}

	rec = httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/loans", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "returnedAt") {
		t.Fatalf("outstanding loans carry a returnedAt key: %s", rec.Body.String())
	}

	var listed []Loan
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decoding loans: %v (body %s)", err, rec.Body.String())
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d loans, want 2", len(listed))
	}
	for i, loan := range listed {
		wantBookID := i + 1
		if loan.ID != i+1 {
			t.Errorf("loan %d: id = %d, want %d", i, loan.ID, i+1)
		}
		if loan.BookID != wantBookID {
			t.Errorf("loan %d: bookId = %d, want %d", i, loan.BookID, wantBookID)
		}
		if loan.BorrowedAt.IsZero() {
			t.Errorf("loan %d: borrowedAt is zero", i)
		}
		if got, want := loan.DueAt.Sub(loan.BorrowedAt), loanPeriod; got != want {
			t.Errorf("loan %d: dueAt - borrowedAt = %v, want %v", i, got, want)
		}
	}

	for _, want := range listed {
		rec := httptest.NewRecorder()
		path := fmt.Sprintf("/loans/%d", want.ID)
		NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "returnedAt") {
			t.Fatalf("%s: outstanding loan carries a returnedAt key: %s", path, rec.Body.String())
		}

		var got Loan
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decoding %s: %v (body %s)", path, err, rec.Body.String())
		}
		if got.ID != want.ID || got.BookID != want.BookID {
			t.Fatalf("%s: loan = %+v, want id %d bookId %d", path, got, want.ID, want.BookID)
		}
	}

	cases := []struct {
		name      string
		path      string
		wantCode  int
		wantError string
	}{
		{
			name:      "unknown id",
			path:      "/loans/99",
			wantCode:  http.StatusNotFound,
			wantError: "loan not found",
		},
		{
			name:      "non-numeric id",
			path:      "/loans/abc",
			wantCode:  http.StatusBadRequest,
			wantError: "invalid loan id",
		},
		{
			name:      "zero id",
			path:      "/loans/0",
			wantCode:  http.StatusBadRequest,
			wantError: "invalid loan id",
		},
		{
			name:      "negative id",
			path:      "/loans/-1",
			wantCode:  http.StatusBadRequest,
			wantError: "invalid loan id",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

			if rec.Code != tc.wantCode {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"error":"`+tc.wantError+`"`) {
				t.Fatalf("body %s, want error %q", rec.Body.String(), tc.wantError)
			}
		})
	}

	if len(loans) != 2 {
		t.Errorf("recorded %d loans, want 2", len(loans))
	}
}

func TestReturnLoanMarksBookAvailable(t *testing.T) {
	resetLoanStore(t)

	borrowedAt := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	loans = []Loan{
		{ID: 1, BookID: 7, BorrowedAt: borrowedAt, DueAt: borrowedAt.Add(loanPeriod)},
		{ID: 2, BookID: 8, BorrowedAt: borrowedAt, DueAt: borrowedAt.Add(loanPeriod)},
	}
	nextLoanID = 3

	var mu sync.Mutex
	var calls []string
	var patchedBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()

		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/books/7/availability":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("reading patch body: %v", err)
			}
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("patch Content-Type = %q, want application/json", got)
			}
			mu.Lock()
			patchedBody = string(body)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":7,"title":"Dune","available":true}`)
		default:
			t.Errorf("unexpected catalog request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	t.Setenv("CATALOG_URL", srv.URL)

	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/loans/1/return", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}

	var returned Loan
	if err := json.Unmarshal(rec.Body.Bytes(), &returned); err != nil {
		t.Fatalf("decoding loan: %v (body %s)", err, rec.Body.String())
	}
	if returned.ID != 1 {
		t.Errorf("id = %d, want 1", returned.ID)
	}
	if returned.BookID != 7 {
		t.Errorf("bookId = %d, want 7", returned.BookID)
	}
	if !returned.BorrowedAt.Equal(borrowedAt) {
		t.Errorf("borrowedAt = %v, want %v", returned.BorrowedAt, borrowedAt)
	}
	if !returned.DueAt.Equal(borrowedAt.Add(loanPeriod)) {
		t.Errorf("dueAt = %v, want %v", returned.DueAt, borrowedAt.Add(loanPeriod))
	}
	if returned.ReturnedAt == nil || returned.ReturnedAt.IsZero() {
		t.Fatalf("returnedAt = %v, want a timestamp", returned.ReturnedAt)
	}

	mu.Lock()
	gotCalls := append([]string(nil), calls...)
	gotBody := patchedBody
	mu.Unlock()

	wantCalls := []string{"PATCH /books/7/availability"}
	if len(gotCalls) != len(wantCalls) || gotCalls[0] != wantCalls[0] {
		t.Fatalf("catalog calls = %v, want %v", gotCalls, wantCalls)
	}

	var patchPayload struct {
		Available *bool `json:"available"`
	}
	if err := json.Unmarshal([]byte(gotBody), &patchPayload); err != nil {
		t.Fatalf("decoding patch body %q: %v", gotBody, err)
	}
	if patchPayload.Available == nil || !*patchPayload.Available {
		t.Fatalf("patch body = %q, want available true", gotBody)
	}

	rec = httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/loans", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("listing loans: status %d, body %s", rec.Code, rec.Body.String())
	}

	var listed []Loan
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decoding loans: %v (body %s)", err, rec.Body.String())
	}
	if len(listed) != 2 {
		t.Fatalf("listed %d loans, want 2", len(listed))
	}
	if listed[0].ID != 1 || listed[0].ReturnedAt == nil {
		t.Errorf("loan 1 = %+v, want it returned with a returnedAt", listed[0])
	}
	if listed[1].ID != 2 || listed[1].ReturnedAt != nil {
		t.Errorf("loan 2 = %+v, want it still outstanding", listed[1])
	}
}

func TestReturnLoanErrorCases(t *testing.T) {
	borrowedAt := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	outstanding := func() {
		loans = []Loan{{ID: 1, BookID: 7, BorrowedAt: borrowedAt, DueAt: borrowedAt.Add(loanPeriod)}}
		nextLoanID = 2
	}
	alreadyReturned := func() {
		outstanding()
		returnedAt := time.Date(2024, 5, 20, 9, 0, 0, 0, time.UTC)
		loans[0].ReturnedAt = &returnedAt
	}

	availableCatalog := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":7,"title":"Dune","available":true}`)
	})
	failingPatchCatalog := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	cases := []struct {
		name                 string
		path                 string
		seed                 func()
		catalog              http.Handler
		wantCatalogHits      int
		wantStatus           int
		wantError            string
		wantStillOutstanding bool
	}{
		{
			name:                 "non-numeric loan id",
			path:                 "/loans/abc/return",
			seed:                 outstanding,
			catalog:              availableCatalog,
			wantCatalogHits:      0,
			wantStatus:           http.StatusBadRequest,
			wantError:            "invalid loan id",
			wantStillOutstanding: true,
		},
		{
			name:                 "zero loan id",
			path:                 "/loans/0/return",
			seed:                 outstanding,
			catalog:              availableCatalog,
			wantCatalogHits:      0,
			wantStatus:           http.StatusBadRequest,
			wantError:            "invalid loan id",
			wantStillOutstanding: true,
		},
		{
			name:                 "negative loan id",
			path:                 "/loans/-1/return",
			seed:                 outstanding,
			catalog:              availableCatalog,
			wantCatalogHits:      0,
			wantStatus:           http.StatusBadRequest,
			wantError:            "invalid loan id",
			wantStillOutstanding: true,
		},
		{
			name:                 "unknown loan id",
			path:                 "/loans/99/return",
			seed:                 outstanding,
			catalog:              availableCatalog,
			wantCatalogHits:      0,
			wantStatus:           http.StatusNotFound,
			wantError:            "loan not found",
			wantStillOutstanding: true,
		},
		{
			name:                 "already returned",
			path:                 "/loans/1/return",
			seed:                 alreadyReturned,
			catalog:              availableCatalog,
			wantCatalogHits:      0,
			wantStatus:           http.StatusConflict,
			wantError:            "loan already returned",
			wantStillOutstanding: false,
		},
		{
			name:                 "catalog rejects the availability patch",
			path:                 "/loans/1/return",
			seed:                 outstanding,
			catalog:              failingPatchCatalog,
			wantCatalogHits:      1,
			wantStatus:           http.StatusBadGateway,
			wantError:            "catalog unavailable",
			wantStillOutstanding: true,
		},
		{
			name:                 "unreachable catalog",
			path:                 "/loans/1/return",
			seed:                 outstanding,
			catalog:              nil,
			wantCatalogHits:      0,
			wantStatus:           http.StatusBadGateway,
			wantError:            "catalog unavailable",
			wantStillOutstanding: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetLoanStore(t)
			tc.seed()

			var mu sync.Mutex
			var hits int
			catalogURL := closedPortURL(t)
			if tc.catalog != nil {
				srv := httptest.NewServer(recordingCatalog(&hits, &mu, tc.catalog))
				defer srv.Close()
				catalogURL = srv.URL
			}
			t.Setenv("CATALOG_URL", catalogURL)

			rec := httptest.NewRecorder()
			NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, nil))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"error":"`+tc.wantError+`"`) {
				t.Fatalf("body %s, want error %q", rec.Body.String(), tc.wantError)
			}

			mu.Lock()
			gotHits := hits
			mu.Unlock()
			if gotHits != tc.wantCatalogHits {
				t.Errorf("catalog received %d requests, want %d", gotHits, tc.wantCatalogHits)
			}

			if tc.wantStillOutstanding {
				rec := httptest.NewRecorder()
				NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/loans/1", nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("GET /loans/1: status %d, body %s", rec.Code, rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), "returnedAt") {
					t.Fatalf("failed return still stamped the loan: %s", rec.Body.String())
				}
			}
		})
	}
}
