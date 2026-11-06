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
