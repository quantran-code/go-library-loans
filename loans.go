package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// loanPeriod is how long a borrowed book may be kept.
const loanPeriod = 14 * 24 * time.Hour

// defaultCatalogURL is the catalogue the loans service talks to when
// CATALOG_URL is not set.
const defaultCatalogURL = "http://localhost:8081"

// Loan is a book taken out of the catalogue.
type Loan struct {
	ID         int       `json:"id"`
	BookID     int       `json:"bookId"`
	BorrowedAt time.Time `json:"borrowedAt"`
	DueAt      time.Time `json:"dueAt"`
}

var (
	// loans is the in-memory store of recorded borrows.
	loans []Loan
	// nextLoanID is the id handed to the next recorded loan.
	nextLoanID = 1
)

// catalogBook is the part of the catalogue's book representation a borrow needs.
type catalogBook struct {
	ID        int  `json:"id"`
	Available bool `json:"available"`
}

// catalogURLFromEnv returns CATALOG_URL when set, otherwise defaultCatalogURL.
func catalogURLFromEnv() string {
	if u := os.Getenv("CATALOG_URL"); u != "" {
		return u
	}
	return defaultCatalogURL
}

// writeJSON writes payload as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// newBorrowHandler records a borrow and takes the book out of the catalogue.
func newBorrowHandler(catalogURL string) http.HandlerFunc {
	base := strings.TrimRight(catalogURL, "/")

	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			BookID int `json:"bookId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.BookID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid book id"})
			return
		}

		book, status, message := fetchCatalogBook(r, base, req.BookID)
		if message != "" {
			writeJSON(w, status, map[string]string{"error": message})
			return
		}
		if !book.Available {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "book already on loan"})
			return
		}

		if status, message := markBookUnavailable(r, base, req.BookID); message != "" {
			writeJSON(w, status, map[string]string{"error": message})
			return
		}

		borrowedAt := time.Now().UTC()
		loan := Loan{
			ID:         nextLoanID,
			BookID:     req.BookID,
			BorrowedAt: borrowedAt,
			DueAt:      borrowedAt.Add(loanPeriod),
		}
		nextLoanID++
		loans = append(loans, loan)

		writeJSON(w, http.StatusCreated, loan)
	}
}

// fetchCatalogBook reads the book from the catalogue. On failure it returns a
// status and error message for the caller to write.
func fetchCatalogBook(r *http.Request, base string, id int) (catalogBook, int, string) {
	var book catalogBook

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, fmt.Sprintf("%s/books/%d", base, id), nil)
	if err != nil {
		return book, http.StatusBadGateway, "catalog unavailable"
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return book, http.StatusBadGateway, "catalog unavailable"
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return book, http.StatusNotFound, "book not found"
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return book, http.StatusBadGateway, "catalog unavailable"
	}
	if err := json.NewDecoder(resp.Body).Decode(&book); err != nil {
		return book, http.StatusBadGateway, "catalog unavailable"
	}
	return book, 0, ""
}

// markBookUnavailable flips the book's availability in the catalogue. On
// failure it returns a status and error message for the caller to write.
func markBookUnavailable(r *http.Request, base string, id int) (int, string) {
	body := []byte(`{"available":false}`)

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPatch, fmt.Sprintf("%s/books/%d/availability", base, id), bytes.NewReader(body))
	if err != nil {
		return http.StatusBadGateway, "catalog unavailable"
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return http.StatusBadGateway, "catalog unavailable"
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return http.StatusBadGateway, "catalog unavailable"
	}
	return 0, ""
}
