package main

import (
	"sync"
	"time"
)

// Loan is a recorded borrow, serialised with the field names the API speaks.
type Loan struct {
	ID         int       `json:"id"`
	BookID     int       `json:"bookId"`
	BorrowedAt time.Time `json:"borrowedAt"`
	DueAt      time.Time `json:"dueAt"`
}

// LoanStore records loans. The handler depends on this interface so a store
// whose Record fails can be injected in tests.
type LoanStore interface {
	Record(bookID int, borrowedAt time.Time) (Loan, error)
}

// MemoryLoanStore keeps the recorded loans in memory, numbered from 1.
type MemoryLoanStore struct {
	mu     sync.Mutex
	loans  []Loan
	nextID int
}

// NewMemoryLoanStore returns an empty store whose first loan gets id 1.
func NewMemoryLoanStore() *MemoryLoanStore {
	return &MemoryLoanStore{nextID: 1}
}

// Record stores a loan of bookID stamped with the supplied borrow time and a
// due date fourteen days later.
func (s *MemoryLoanStore) Record(bookID int, borrowedAt time.Time) (Loan, error) {
	borrowed := borrowedAt.UTC().Truncate(time.Second)
	due := borrowed.AddDate(0, 0, 14)

	s.mu.Lock()
	defer s.mu.Unlock()

	loan := Loan{
		ID:         s.nextID,
		BookID:     bookID,
		BorrowedAt: borrowed,
		DueAt:      due,
	}
	s.nextID++
	s.loans = append(s.loans, loan)

	return loan, nil
}
