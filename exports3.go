package main

import (
	"crypto/rand"
	"fmt"
	"net/http"
	"time"
)

// loansExportPrefix is the key prefix every loans export is stored under.
const loansExportPrefix = "exports/loans/"

// exportS3Response is the body POST /loans/export-s3 answers with.
type exportS3Response struct {
	Key       string `json:"key"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
	Size      int64  `json:"size"`
}

// newExportKey returns prefix followed by a random version 4 UUID and the .csv
// extension, so an export never overwrites an earlier object.
func newExportKey(prefix string) (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generating export key: %w", err)
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80

	return fmt.Sprintf("%s%x-%x-%x-%x-%x.csv", prefix, id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]), nil
}

// exportLoansToS3 renders every loan with renderLoansCSV, stores the buffer
// once under a server-generated key and answers a presigned GET for it.
func exportLoansToS3(store LoanStore, storage objectStorage, now func() time.Time) http.HandlerFunc {
	if now == nil {
		now = time.Now
	}

	return func(w http.ResponseWriter, r *http.Request) {
		if storage == nil {
			writeStorageFailure(w, errStorageNotConfigured)
			return
		}

		body, err := renderLoansCSV(store.List())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not render loans"})
			return
		}

		key, err := newExportKey(loansExportPrefix)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not export loans"})
			return
		}

		ctx := r.Context()

		// The whole CSV is in body before the single put, so a failure here
		// leaves no partial object.
		if err := storage.Put(ctx, key, body, csvContentType); err != nil {
			if !writeStorageFailure(w, err) {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "storage unavailable"})
			}
			return
		}

		ttl := presignTTLFor(storage)
		url, err := storage.PresignGet(ctx, key, ttl)
		if err != nil {
			if !writeStorageFailure(w, err) {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "storage unavailable"})
			}
			return
		}

		expiresAt := now().UTC().Truncate(time.Second).Add(ttl)

		writeJSON(w, http.StatusCreated, exportS3Response{
			Key:       key,
			URL:       url,
			ExpiresAt: formatTimestamp(expiresAt),
			Size:      int64(len(body)),
		})
	}
}
