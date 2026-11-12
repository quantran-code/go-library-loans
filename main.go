// Command loans records which member borrowed which book.
package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

const defaultCatalogURL = "http://localhost:8081"

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}
	log.Printf("loans listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, NewRouter()))
}

// catalogURL returns the catalogue's address, defaulting to the local port.
func catalogURL() string {
	if url := os.Getenv("CATALOG_URL"); url != "" {
		return url
	}
	return defaultCatalogURL
}

// NewRouter wires the loan API.
func NewRouter() http.Handler {
	return newRouter(NewMemoryLoanStore(), NewCatalogClient(catalogURL()))
}

// newRouter wires the loan API around the supplied store and catalogue client.
func newRouter(store LoanStore, catalog *CatalogClient) http.Handler {
	r := chi.NewRouter()
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Post("/loans", createLoan(store, catalog))
	r.Get("/loans", listLoans(store))
	r.Get("/loans/{id}", getLoan(store))
	r.Post("/loans/{id}/return", returnLoan(store, catalog))
	return r
}

// createLoan takes the copy off the shelf and records the loan, putting the
// copy back when recording fails.
func createLoan(store LoanStore, catalog *CatalogClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dec := json.NewDecoder(r.Body)

		var body map[string]json.RawMessage
		if err := dec.Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		raw, ok := body["bookId"]
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid book id"})
			return
		}
		var bookID int
		if err := json.Unmarshal(raw, &bookID); err != nil || bookID < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid book id"})
			return
		}

		ctx := r.Context()

		book, found, err := catalog.GetBook(ctx, bookID)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "catalog unavailable"})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "book not found"})
			return
		}
		if !book.Available {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "book already on loan"})
			return
		}

		if err := catalog.SetAvailability(ctx, bookID, false); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "catalog unavailable"})
			return
		}

		loan, err := store.Record(bookID, time.Now())
		if err != nil {
			// Recording failed, so put the copy back on the shelf before
			// telling the caller the loan was not recorded.
			_ = catalog.SetAvailability(ctx, bookID, true)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not record loan"})
			return
		}

		writeJSON(w, http.StatusCreated, loan)
	}
}

// parseLoanID reads the {id} path segment, rejecting anything that is not a
// positive whole number.
func parseLoanID(r *http.Request) (int, error) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil || id < 1 {
		return 0, errors.New("invalid loan id")
	}

	return id, nil
}

// listLoans answers every recorded loan, newest first. An empty store answers
// [] rather than null.
func listLoans(store LoanStore) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, store.List())
	}
}

// getLoan answers one recorded loan.
func getLoan(store LoanStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := parseLoanID(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid loan id"})
			return
		}

		loan, found := store.Get(id)
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "loan not found"})
			return
		}

		writeJSON(w, http.StatusOK, loan)
	}
}

// returnLoan puts the copy back on the shelf and stamps the loan returned.
// The catalogue is updated first so a failure there leaves the loan open.
func returnLoan(store LoanStore, catalog *CatalogClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := parseLoanID(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid loan id"})
			return
		}

		loan, found := store.Get(id)
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "loan not found"})
			return
		}
		if loan.ReturnedAt != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "loan already returned"})
			return
		}

		ctx := r.Context()

		book, found, err := catalog.GetBook(ctx, loan.BookID)
		if err != nil || !found {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "catalog unavailable"})
			return
		}
		if book.Available {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "book not on loan"})
			return
		}

		if err := catalog.SetAvailability(ctx, loan.BookID, true); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "catalog unavailable"})
			return
		}

		returned, err := store.MarkReturned(id, time.Now())
		if err != nil {
			switch {
			case errors.Is(err, ErrLoanAlreadyReturned):
				// Another return recorded this loan first, so the copy this
				// request just put back belongs to that return: take it off
				// the shelf again before refusing.
				_ = catalog.SetAvailability(ctx, loan.BookID, false)
				writeJSON(w, http.StatusConflict, map[string]string{"error": "loan already returned"})
			case errors.Is(err, ErrLoanNotFound):
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "loan not found"})
			default:
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not record return"})
			}
			return
		}

		writeJSON(w, http.StatusOK, returned)
	}
}

// writeJSON writes body as the JSON answer with the supplied status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
