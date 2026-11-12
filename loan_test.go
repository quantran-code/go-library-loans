package main

import (
	"encoding/json"
	"errors"
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

// loanJSON encodes loan and returns the decoded object plus its sorted keys.
func loanJSON(t *testing.T, loan Loan) (map[string]any, []string) {
	t.Helper()

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

	return decoded, keys
}

func TestLoanJSONOmitsReturnedAtUntilReturned(t *testing.T) {
	loan := Loan{
		ID:         1,
		BookID:     1,
		BorrowedAt: time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC),
		DueAt:      time.Date(2024, 5, 15, 10, 0, 0, 0, time.UTC),
	}

	decoded, keys := loanJSON(t, loan)

	want := []string{"bookId", "borrowedAt", "dueAt", "id"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("loan keys %v, want %v", keys, want)
	}
	if _, ok := decoded["returnedAt"]; ok {
		t.Fatalf("active loan carries the key returnedAt: %v", decoded)
	}
}

func TestLoanJSONIncludesReturnedAtWhenReturned(t *testing.T) {
	returnedAt := time.Date(2024, 5, 10, 9, 30, 0, 0, time.UTC)
	loan := Loan{
		ID:         1,
		BookID:     1,
		BorrowedAt: time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC),
		DueAt:      time.Date(2024, 5, 15, 10, 0, 0, 0, time.UTC),
		ReturnedAt: &returnedAt,
	}

	decoded, _ := loanJSON(t, loan)

	stamp, ok := decoded["returnedAt"].(string)
	if !ok {
		t.Fatalf("returnedAt is not a string in %v", decoded)
	}
	if stamp != "2024-05-10T09:30:00Z" {
		t.Fatalf("returnedAt %q, want 2024-05-10T09:30:00Z", stamp)
	}
	if _, err := time.Parse(time.RFC3339, stamp); err != nil {
		t.Fatalf("returnedAt %q does not parse as RFC 3339: %v", stamp, err)
	}
}

func TestListReturnsNewestFirstAndEmptyForNoLoans(t *testing.T) {
	store := NewMemoryLoanStore()
	borrowedAt := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)

	empty := store.List()
	if empty == nil {
		t.Fatalf("List on a fresh store returned a nil slice")
	}
	encoded, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("encoding empty list: %v", err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("empty list encodes as %s, want []", encoded)
	}

	if _, err := store.Record(1, borrowedAt); err != nil {
		t.Fatalf("recording first loan: %v", err)
	}
	if _, err := store.Record(2, borrowedAt); err != nil {
		t.Fatalf("recording second loan: %v", err)
	}

	loans := store.List()
	if len(loans) != 2 {
		t.Fatalf("List returned %d loans, want 2", len(loans))
	}
	if loans[0].ID != 2 {
		t.Fatalf("first listed loan id %d, want 2", loans[0].ID)
	}
	if loans[1].ID != 1 {
		t.Fatalf("second listed loan id %d, want 1", loans[1].ID)
	}
}

func TestMarkReturnedStampsReturnedAtAndRefusesTwice(t *testing.T) {
	store := NewMemoryLoanStore()
	borrowedAt := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	if _, err := store.Record(5, borrowedAt); err != nil {
		t.Fatalf("recording loan: %v", err)
	}

	supplied := time.Date(2024, 5, 10, 9, 30, 0, 987654321, time.FixedZone("CEST", 2*60*60))
	returned, err := store.MarkReturned(1, supplied)
	if err != nil {
		t.Fatalf("marking loan returned: %v", err)
	}
	if returned.ReturnedAt == nil {
		t.Fatalf("loan %+v has no returnedAt", returned)
	}
	want := time.Date(2024, 5, 10, 7, 30, 0, 0, time.UTC)
	if returned.ReturnedAt.Location() != time.UTC {
		t.Fatalf("returnedAt location %v, want UTC", returned.ReturnedAt.Location())
	}
	if returned.ReturnedAt.Nanosecond() != 0 {
		t.Fatalf("returnedAt nanoseconds %d, want 0", returned.ReturnedAt.Nanosecond())
	}
	if !returned.ReturnedAt.Equal(want) {
		t.Fatalf("returnedAt %v, want %v", returned.ReturnedAt, want)
	}

	stored, found := store.Get(1)
	if !found {
		t.Fatalf("Get(1) did not find the recorded loan")
	}
	if stored.ReturnedAt == nil || !stored.ReturnedAt.Equal(want) {
		t.Fatalf("stored returnedAt %v, want %v", stored.ReturnedAt, want)
	}

	if _, err := store.MarkReturned(1, time.Date(2024, 6, 1, 8, 0, 0, 0, time.UTC)); !errors.Is(err, ErrLoanAlreadyReturned) {
		t.Fatalf("second MarkReturned error %v, want %v", err, ErrLoanAlreadyReturned)
	}

	after, found := store.Get(1)
	if !found {
		t.Fatalf("Get(1) did not find the recorded loan after the refused return")
	}
	if after.ReturnedAt == nil || !after.ReturnedAt.Equal(want) {
		t.Fatalf("returnedAt changed to %v after a refused return, want %v", after.ReturnedAt, want)
	}
}

func TestMarkReturnedAndGetRejectAnUnknownLoan(t *testing.T) {
	store := NewMemoryLoanStore()

	if _, err := store.MarkReturned(42, time.Date(2024, 5, 10, 9, 30, 0, 0, time.UTC)); !errors.Is(err, ErrLoanNotFound) {
		t.Fatalf("MarkReturned error %v, want %v", err, ErrLoanNotFound)
	}

	if loan, found := store.Get(42); found {
		t.Fatalf("Get(42) found %+v, want no loan", loan)
	}
}
