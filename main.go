// Command loans records which member borrowed which book.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8082"
	}
	log.Printf("loans listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, NewRouter()))
}

// NewRouter wires the loan API.
func NewRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	r.With(withRequestTrace).Post("/loans", newBorrowHandler(catalogURLFromEnv()))
	r.With(withRequestTrace).Get("/loans", newListLoansHandler())
	r.With(withRequestTrace).Get("/loans/{id}", newGetLoanHandler())
	r.With(withRequestTrace).Post("/loans/{id}/return", newReturnHandler(catalogURLFromEnv()))
	return r
}
