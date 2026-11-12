package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetBookDistinguishesMissingFromUnavailable(t *testing.T) {
	present := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/books/7" {
			t.Errorf("path %q, want /books/7", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":7,"title":"Dune","author":"Frank Herbert","available":false}`))
	}))
	defer present.Close()

	book, found, err := NewCatalogClient(present.URL).GetBook(context.Background(), 7)
	if err != nil {
		t.Fatalf("getting present book: %v", err)
	}
	if !found {
		t.Fatalf("found = false for a book the catalogue returned")
	}
	if book.ID != 7 || book.Title != "Dune" || book.Available {
		t.Fatalf("unexpected book %+v", book)
	}

	missing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer missing.Close()

	if _, found, err := NewCatalogClient(missing.URL).GetBook(context.Background(), 7); err != nil {
		t.Fatalf("getting missing book: %v", err)
	} else if found {
		t.Fatalf("found = true for a 404 answer")
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()

	if _, _, err := NewCatalogClient(failing.URL).GetBook(context.Background(), 7); err == nil {
		t.Fatalf("expected an error for a 500 answer")
	}

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	if _, _, err := NewCatalogClient(closedURL).GetBook(context.Background(), 7); err == nil {
		t.Fatalf("expected an error for an unreachable catalogue")
	}
}

func TestSetAvailabilityPatchesTheCatalogue(t *testing.T) {
	type request struct {
		method string
		path   string
		body   map[string]bool
	}

	var seen []request
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]bool
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		seen = append(seen, request{method: r.Method, path: r.URL.Path, body: body})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":7,"available":false}`))
	}))
	defer stub.Close()

	if err := NewCatalogClient(stub.URL).SetAvailability(context.Background(), 7, false); err != nil {
		t.Fatalf("setting availability: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("catalogue saw %d requests, want 1", len(seen))
	}
	if seen[0].method != http.MethodPatch {
		t.Fatalf("method %q, want %q", seen[0].method, http.MethodPatch)
	}
	if seen[0].path != "/books/7/availability" {
		t.Fatalf("path %q, want /books/7/availability", seen[0].path)
	}
	if available, ok := seen[0].body["available"]; !ok {
		t.Fatalf("body %v has no available field", seen[0].body)
	} else if available {
		t.Fatalf("available = true, want false")
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()

	if err := NewCatalogClient(failing.URL).SetAvailability(context.Background(), 7, false); err == nil {
		t.Fatalf("expected an error for a 500 answer")
	}

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	if err := NewCatalogClient(closedURL).SetAvailability(context.Background(), 7, false); err == nil {
		t.Fatalf("expected an error for an unreachable catalogue")
	}
}
