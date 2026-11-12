package main

import (
	"errors"
	"sync"
	"time"
)

// Loan is a recorded borrow, serialised with the field names the API speaks.
// ReturnedAt stays out of the JSON while the copy is still out.
type Loan struct {
	ID         int        `json:"id"`
	BookID     int        `json:"bookId"`
	BorrowedAt time.Time  `json:"borrowedAt"`
	DueAt      time.Time  `json:"dueAt"`
	ReturnedAt *time.Time `json:"returnedAt,omitempty"`
}

var (
	// ErrLoanNotFound is returned by MarkReturned when no loan has that id.
	ErrLoanNotFound = errors.New("loan not found")
	// ErrLoanAlreadyReturned is returned by MarkReturned when the loan already
	// carries a return time.
	ErrLoanAlreadyReturned = errors.New("loan already returned")
)

// LoanStore records loans. The handler depends on this interface so a store
// whose Record fails can be injected in tests.
type LoanStore interface {
	Record(bookID int, borrowedAt time.Time) (Loan, error)
	List() []Loan
	Get(id int) (Loan, bool)
	MarkReturned(id int, returnedAt time.Time) (Loan, error)
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

// List returns the recorded loans newest first. The slice is never nil, so an
// empty store serialises to [] rather than null.
func (s *MemoryLoanStore) List() []Loan {
	s.mu.Lock()
	defer s.mu.Unlock()

	loans := make([]Loan, 0, len(s.loans))
	for i := len(s.loans) - 1; i >= 0; i-- {
		loans = append(loans, s.loans[i])
	}

	return loans
}

// Get returns the loan with that id, reporting found as false when there is
// none.
func (s *MemoryLoanStore) Get(id int) (Loan, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, loan := range s.loans {
		if loan.ID == id {
			return loan, true
		}
	}

	return Loan{}, false
}

// MarkReturned stamps the loan's return time, refusing an unknown id and a
// loan that has already come back.
func (s *MemoryLoanStore) MarkReturned(id int, returnedAt time.Time) (Loan, error) {
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
