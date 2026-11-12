package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"
)

const (
	// loansExportFilename is the name the loans export is saved under.
	loansExportFilename = "loans.csv"
	// csvContentType is the type every CSV export answers with.
	csvContentType = "text/csv; charset=utf-8"
)

// loansCSVHeader is the header record of the loans export, in order. Go has no
// constant slices, so it is a package-level value that callers must not mutate.
var loansCSVHeader = []string{"id", "bookId", "borrowedAt", "dueAt", "returnedAt"}

// formatTimestamp renders t as RFC 3339 in UTC at second precision.
func formatTimestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// renderLoansCSV renders every loan as loans.csv, newest loan first. Quoting
// follows RFC 4180 and records are CRLF terminated. An empty slice renders the
// header record only.
func renderLoansCSV(loans []Loan) ([]byte, error) {
	ordered := make([]Loan, len(loans))
	copy(ordered, loans)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].ID > ordered[j].ID
	})

	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	writer.UseCRLF = true

	if err := writer.Write(loansCSVHeader); err != nil {
		return nil, fmt.Errorf("writing loans csv header: %w", err)
	}

	for _, loan := range ordered {
		returnedAt := ""
		if loan.ReturnedAt != nil {
			returnedAt = formatTimestamp(*loan.ReturnedAt)
		}

		record := []string{
			strconv.Itoa(loan.ID),
			strconv.Itoa(loan.BookID),
			formatTimestamp(loan.BorrowedAt),
			formatTimestamp(loan.DueAt),
			returnedAt,
		}
		if err := writer.Write(record); err != nil {
			return nil, fmt.Errorf("writing loan %d: %w", loan.ID, err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("writing loans csv: %w", err)
	}

	return buf.Bytes(), nil
}

// exportLoans answers every recorded loan as a CSV attachment, newest first.
// The body is rendered into a buffer first so a partial file is never sent.
func exportLoans(store LoanStore) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		body, err := renderLoansCSV(store.List())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not render loans"})
			return
		}

		w.Header().Set("Content-Type", csvContentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", loansExportFilename))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}
