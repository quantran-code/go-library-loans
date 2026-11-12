package main

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestRecordNumbersLoansFromOne(t *testing.T) {
	store := NewMemoryLoanStore()
	borrowedAt := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)

	first, err := store.Record(1, borrowedAt)
	if err != nil {
		t.Fatalf("recording first loan: %v", err)
	}
	second, err := store.Record(2, borrowedAt)
	if err != nil {
		t.Fatalf("recording second loan: %v", err)
	}

	if first.ID != 1 {
		t.Fatalf("first loan id %d, want 1", first.ID)
	}
	if second.ID != 2 {
		t.Fatalf("second loan id %d, want 2", second.ID)
	}
	if first.BookID != 1 {
		t.Fatalf("first loan book id %d, want 1", first.BookID)
	}
	if second.BookID != 2 {
		t.Fatalf("second loan book id %d, want 2", second.BookID)
	}
}

func TestRecordSetsDueAtFourteenDaysAfterBorrowedAt(t *testing.T) {
	store := NewMemoryLoanStore()
	supplied := time.Date(2024, 5, 1, 10, 0, 0, 123456789, time.FixedZone("CEST", 2*60*60))

	loan, err := store.Record(3, supplied)
	if err != nil {
		t.Fatalf("recording loan: %v", err)
	}

	if loan.BorrowedAt.Location() != time.UTC {
		t.Fatalf("borrowedAt location %v, want UTC", loan.BorrowedAt.Location())
	}
	if loan.BorrowedAt.Nanosecond() != 0 {
		t.Fatalf("borrowedAt nanoseconds %d, want 0", loan.BorrowedAt.Nanosecond())
	}
	if got := loan.DueAt.Sub(loan.BorrowedAt); got != 14*24*time.Hour {
		t.Fatalf("dueAt - borrowedAt = %v, want %v", got, 14*24*time.Hour)
	}
}

func TestLoanJSONUsesTheAgreedFieldNames(t *testing.T) {
	loan := Loan{
		ID:         1,
		BookID:     1,
		BorrowedAt: time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC),
		DueAt:      time.Date(2024, 5, 15, 10, 0, 0, 0, time.UTC),
	}

	encoded, err := json.Marshal(loan)
	if err != nil {
		t.Fatalf("encoding loan: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decoding loan %s: %v", encoded, err)
	}

	keys := make([]string, 0, len(decoded))
	for key := range decoded {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	want := []string{"bookId", "borrowedAt", "dueAt", "id"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("loan keys %v, want %v", keys, want)
	}

	borrowedAt, ok := decoded["borrowedAt"].(string)
	if !ok {
		t.Fatalf("borrowedAt is not a string in %s", encoded)
	}
	if borrowedAt != "2024-05-01T10:00:00Z" {
		t.Fatalf("borrowedAt %q, want 2024-05-01T10:00:00Z", borrowedAt)
	}
	if _, err := time.Parse(time.RFC3339, borrowedAt); err != nil {
		t.Fatalf("borrowedAt %q does not parse as RFC 3339: %v", borrowedAt, err)
	}
}
