package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Book mirrors the catalogue's book JSON so a GET /books/{id} decodes directly.
type Book struct {
	ID        int    `json:"id"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	Available bool   `json:"available"`
}

// CatalogClient talks to the catalogue service over HTTP.
type CatalogClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewCatalogClient returns a client for the catalogue served at baseURL.
func NewCatalogClient(baseURL string) *CatalogClient {
	return &CatalogClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// GetBook reads one book from the catalogue. found is false when the catalogue
// answers 404; any other non-2xx answer or transport failure is an error.
func (c *CatalogClient) GetBook(ctx context.Context, id int) (Book, bool, error) {
	url := fmt.Sprintf("%s/books/%d", c.baseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Book{}, false, fmt.Errorf("building request for book %d: %w", id, err)
	}
	if requestID := requestIDFrom(ctx); requestID != "" {
		req.Header.Set(requestIDHeader, requestID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Book{}, false, fmt.Errorf("getting book %d: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return Book{}, false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Book{}, false, fmt.Errorf("getting book %d: unexpected status %d", id, resp.StatusCode)
	}

	var book Book
	if err := json.NewDecoder(resp.Body).Decode(&book); err != nil {
		return Book{}, false, fmt.Errorf("decoding book %d: %w", id, err)
	}

	return book, true, nil
}

// SetAvailability sets the shelf state of one copy through the catalogue's
// availability endpoint.
func (c *CatalogClient) SetAvailability(ctx context.Context, id int, available bool) error {
	body, err := json.Marshal(map[string]bool{"available": available})
	if err != nil {
		return fmt.Errorf("encoding availability for book %d: %w", id, err)
	}

	url := fmt.Sprintf("%s/books/%d/availability", c.baseURL, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building availability request for book %d: %w", id, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if requestID := requestIDFrom(ctx); requestID != "" {
		req.Header.Set(requestIDHeader, requestID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("setting availability for book %d: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("setting availability for book %d: unexpected status %d", id, resp.StatusCode)
	}

	return nil
}
