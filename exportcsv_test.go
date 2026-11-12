package main

import (
	"encoding/csv"
	"net/http"
	"strings"
	"testing"
	"time"
)

// exportRouter wires the loan API around store together with a stub catalogue
// the export route never calls.
func exportRouter(t *testing.T, store LoanStore) http.Handler {
	t.Helper()

	stub := newStubCatalog(t, http.StatusOK, availableBookJSON, http.StatusOK)

	return newRouter(store, NewCatalogClient(stub.server.URL))
}

// readCSV parses an exported body back into its records.
func readCSV(t *testing.T, body string) [][]string {
	t.Helper()

	records, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatalf("reading exported csv %q: %v", body, err)
	}

	return records
}

func TestExportLoansWritesHeaderAndNewestFirst(t *testing.T) {
	store := NewMemoryLoanStore()
	borrowedAt := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	for _, bookID := range []int{3, 1, 2} {
		if _, err := store.Record(bookID, borrowedAt); err != nil {
			t.Fatalf("recording loan: %v", err)
		}
	}

	rec := getPath(t, exportRouter(t, store), "/loans/export")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != csvContentType {
		t.Fatalf("Content-Type %q, want %q", got, csvContentType)
	}
	if got, want := rec.Header().Get("Content-Disposition"), `attachment; filename="loans.csv"`; got != want {
		t.Fatalf("Content-Disposition %q, want %q", got, want)
	}

	records := readCSV(t, rec.Body.String())
	if len(records) != 4 {
		t.Fatalf("export answers %d records, want 4: %v", len(records), records)
	}
	if got := strings.Join(records[0], ","); got != "id,bookId,borrowedAt,dueAt,returnedAt" {
		t.Fatalf("header %q, want id,bookId,borrowedAt,dueAt,returnedAt", got)
	}
	wantIDs := []string{"3", "2", "1"}
	for i, want := range wantIDs {
		if got := records[i+1][0]; got != want {
			t.Fatalf("record %d id %q, want %q", i+1, got, want)
		}
	}
	if !strings.Contains(rec.Body.String(), "\r\n") {
		t.Fatalf("export is not CRLF terminated: %q", rec.Body.String())
	}
}

func TestExportLoansLeavesActiveReturnedAtEmpty(t *testing.T) {
	store := NewMemoryLoanStore()
	borrowedAt := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	if _, err := store.Record(1, borrowedAt); err != nil {
		t.Fatalf("recording first loan: %v", err)
	}
	if _, err := store.Record(2, borrowedAt); err != nil {
		t.Fatalf("recording second loan: %v", err)
	}

	supplied := time.Date(2024, 5, 10, 9, 30, 0, 987654321, time.FixedZone("CEST", 2*60*60))
	if _, err := store.MarkReturned(1, supplied); err != nil {
		t.Fatalf("marking loan returned: %v", err)
	}

	rec := getPath(t, exportRouter(t, store), "/loans/export")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	records := readCSV(t, rec.Body.String())
	if len(records) != 3 {
		t.Fatalf("export answers %d records, want 3: %v", len(records), records)
	}

	byID := make(map[string][]string, len(records)-1)
	for _, record := range records[1:] {
		byID[record[0]] = record
	}

	active, ok := byID["2"]
	if !ok {
		t.Fatalf("export carries no record for loan 2: %v", records)
	}
	if got := active[4]; got != "" {
		t.Fatalf("active loan returnedAt %q, want empty", got)
	}
	if got := active[2]; got != "2024-05-01T10:00:00Z" {
		t.Fatalf("active loan borrowedAt %q, want 2024-05-01T10:00:00Z", got)
	}

	returned, ok := byID["1"]
	if !ok {
		t.Fatalf("export carries no record for loan 1: %v", records)
	}
	if got := returned[4]; got != "2024-05-10T07:30:00Z" {
		t.Fatalf("returned loan returnedAt %q, want 2024-05-10T07:30:00Z", got)
	}
	if got := returned[3]; got != "2024-05-15T10:00:00Z" {
		t.Fatalf("returned loan dueAt %q, want 2024-05-15T10:00:00Z", got)
	}
}

func TestExportLoansOfEmptyStoreIsHeaderOnly(t *testing.T) {
	rec := getPath(t, exportRouter(t, NewMemoryLoanStore()), "/loans/export")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != csvContentType {
		t.Fatalf("Content-Type %q, want %q", got, csvContentType)
	}
	if got, want := rec.Body.String(), "id,bookId,borrowedAt,dueAt,returnedAt\r\n"; got != want {
		t.Fatalf("empty export %q, want %q", got, want)
	}
}

func TestExportLoansRouteIsNotTreatedAsAnId(t *testing.T) {
	rec := getPath(t, exportRouter(t, NewMemoryLoanStore()), "/loans/export")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d for /loans/export, body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("/loans/export answered an error body: %s", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="loans.csv"` {
		t.Fatalf("Content-Disposition %q, want the export attachment", got)
	}
	if !strings.HasPrefix(rec.Body.String(), "id,bookId,borrowedAt,dueAt,returnedAt") {
		t.Fatalf("/loans/export body %q does not start with the export header", rec.Body.String())
	}
}
