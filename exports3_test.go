package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const testPresignURLPrefix = "https://storage.example/"

// storedExportObject is one object the fake storage holds.
type storedExportObject struct {
	data        []byte
	contentType string
}

// fakePresign is one presigned GET the fake storage was asked for.
type fakePresign struct {
	key string
	ttl time.Duration
}

// fakeExportStorage records the objects and presigns the export makes and can
// fail either call on demand.
type fakeExportStorage struct {
	mu         sync.Mutex
	objects    map[string]storedExportObject
	presigns   []fakePresign
	putErr     error
	presignErr error
}

// newFakeExportStorage returns an empty fake object store.
func newFakeExportStorage() *fakeExportStorage {
	return &fakeExportStorage{objects: map[string]storedExportObject{}}
}

// Put stores a copy of data under key, or reports putErr without storing.
func (s *fakeExportStorage) Put(_ context.Context, key string, data []byte, contentType string) error {
	if s.putErr != nil {
		return s.putErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stored := make([]byte, len(data))
	copy(stored, data)
	s.objects[key] = storedExportObject{data: stored, contentType: contentType}

	return nil
}

// PresignGet records the request and answers a fixed URL for key.
func (s *fakeExportStorage) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	if s.presignErr != nil {
		return "", s.presignErr
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.presigns = append(s.presigns, fakePresign{key: key, ttl: ttl})

	return testPresignURLPrefix + key + "?X-Amz-Signature=fake", nil
}

// object returns the object stored under key.
func (s *fakeExportStorage) object(key string) (storedExportObject, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	object, ok := s.objects[key]

	return object, ok
}

// storedKeys returns the keys the fake currently holds.
func (s *fakeExportStorage) storedKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		keys = append(keys, key)
	}

	return keys
}

// presigned returns the presigned GETs the fake was asked for.
func (s *fakeExportStorage) presigned() []fakePresign {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]fakePresign(nil), s.presigns...)
}

// exportS3Router wires the loan API around store with storage as its object
// store.
func exportS3Router(t *testing.T, store LoanStore, storage objectStorage) http.Handler {
	t.Helper()

	stub := newStubCatalog(t, http.StatusOK, availableBookJSON, http.StatusOK)

	return newRouterWithStorage(store, NewCatalogClient(stub.server.URL), storage)
}

// postS3Export sends the bodyless POST the S3 export route takes.
func postS3Export(t *testing.T, router http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))

	return rec
}

// decodeS3Export decodes an export answer into its generic object.
func decodeS3Export(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding export %s: %v", rec.Body.String(), err)
	}

	return body
}

// sortedKeys returns the sorted keys of a decoded JSON object.
func sortedKeys(body map[string]any) []string {
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	return keys
}

// seedLoans records three loans and marks the first returned, so an export has
// an active loan and a returned one to render.
func seedLoans(t *testing.T, store LoanStore) {
	t.Helper()

	borrowedAt := time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)
	for _, bookID := range []int{1, 2, 3} {
		if _, err := store.Record(bookID, borrowedAt); err != nil {
			t.Fatalf("recording loan: %v", err)
		}
	}
	if _, err := store.MarkReturned(1, time.Date(2024, 5, 10, 9, 30, 0, 0, time.UTC)); err != nil {
		t.Fatalf("marking loan returned: %v", err)
	}
}

func TestExportLoansToS3AnswersKeyURLSizeAndExpiry(t *testing.T) {
	store := NewMemoryLoanStore()
	seedLoans(t, store)

	storage := newFakeExportStorage()
	now := time.Date(2024, 6, 1, 12, 0, 0, 500, time.UTC)

	rec := httptest.NewRecorder()
	exportLoansToS3(store, storage, func() time.Time { return now })(
		rec,
		httptest.NewRequest(http.MethodPost, "/loans/export-s3", nil),
	)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	body := decodeS3Export(t, rec)
	want := []string{"expiresAt", "key", "size", "url"}
	if got := sortedKeys(body); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("answer keys %v, want %v", got, want)
	}

	key, _ := body["key"].(string)
	if matched, err := regexp.MatchString(`^exports/loans/[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.csv$`, key); err != nil {
		t.Fatalf("matching key %q: %v", key, err)
	} else if !matched {
		t.Fatalf("key %q is not exports/loans/<uuid>.csv", key)
	}

	url, _ := body["url"].(string)
	wantURL := testPresignURLPrefix + key + "?X-Amz-Signature=fake"
	if url != wantURL {
		t.Fatalf("url %q, want %q", url, wantURL)
	}

	if got := body["expiresAt"].(string); got != "2024-06-01T12:15:00Z" {
		t.Fatalf("expiresAt %q, want 2024-06-01T12:15:00Z", got)
	}

	rendered, err := renderLoansCSV(store.List())
	if err != nil {
		t.Fatalf("rendering loans csv: %v", err)
	}
	if got := body["size"].(float64); int64(got) != int64(len(rendered)) {
		t.Fatalf("size %v, want %d", body["size"], len(rendered))
	}

	object, ok := storage.object(key)
	if !ok {
		t.Fatalf("storage holds no object %q, only %v", key, storage.storedKeys())
	}
	if !bytes.Equal(object.data, rendered) {
		t.Fatalf("stored object %q does not equal the rendered csv", key)
	}
	if object.contentType != csvContentType {
		t.Fatalf("stored content type %q, want %q", object.contentType, csvContentType)
	}

	presigned := storage.presigned()
	if len(presigned) != 1 {
		t.Fatalf("storage saw %d presigns, want 1: %v", len(presigned), presigned)
	}
	if presigned[0].key != key || presigned[0].ttl != defaultPresignTTLSeconds*time.Second {
		t.Fatalf("presign %+v, want key %q and the default ttl", presigned[0], key)
	}
}

func TestExportLoansToS3StoresTheExportCSV(t *testing.T) {
	store := NewMemoryLoanStore()
	seedLoans(t, store)

	storage := newFakeExportStorage()
	router := exportS3Router(t, store, storage)

	exported := postS3Export(t, router, "/loans/export-s3")
	if exported.Code != http.StatusCreated {
		t.Fatalf("status %d, body %s", exported.Code, exported.Body.String())
	}

	key, _ := decodeS3Export(t, exported)["key"].(string)
	object, ok := storage.object(key)
	if !ok {
		t.Fatalf("storage holds no object %q, only %v", key, storage.storedKeys())
	}

	csv := getPath(t, router, "/loans/export")
	if csv.Code != http.StatusOK {
		t.Fatalf("csv export status %d, body %s", csv.Code, csv.Body.String())
	}
	if string(object.data) != csv.Body.String() {
		t.Fatalf("stored object differs from GET /loans/export:\nstored %q\nserved %q", string(object.data), csv.Body.String())
	}

	records := readCSV(t, string(object.data))
	if len(records) != 4 {
		t.Fatalf("export answers %d records, want 4: %v", len(records), records)
	}
	if got := strings.Join(records[0], ","); got != "id,bookId,borrowedAt,dueAt,returnedAt" {
		t.Fatalf("header %q, want id,bookId,borrowedAt,dueAt,returnedAt", got)
	}
	wantIDs := []string{"3", "2", "1"}
	for i, want := range wantIDs {
		if got := records[i+1][0]; got != want {
			t.Fatalf("record %d id %q, want %q", i+1, got, want)
		}
	}
	if got := records[1][4]; got != "" {
		t.Fatalf("active loan returnedAt %q, want empty", got)
	}
	if got := records[3][4]; got != "2024-05-10T09:30:00Z" {
		t.Fatalf("returned loan returnedAt %q, want 2024-05-10T09:30:00Z", got)
	}
	if !strings.Contains(string(object.data), "\r\n") {
		t.Fatalf("export is not CRLF terminated: %q", string(object.data))
	}
}

func TestExportLoansToS3StorageFailureAnswers502(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "unavailable", err: errStorageUnavailable},
		{name: "other failure", err: errors.New("put failed")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemoryLoanStore()
			seedLoans(t, store)

			storage := newFakeExportStorage()
			storage.putErr = tc.err
			router := exportS3Router(t, store, storage)

			rec := postS3Export(t, router, "/loans/export-s3")
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"error":"storage unavailable"`) {
				t.Fatalf("unexpected body: %s", rec.Body.String())
			}
			if keys := storage.storedKeys(); len(keys) != 0 {
				t.Fatalf("failed export stored %v, want nothing", keys)
			}
			if presigned := storage.presigned(); len(presigned) != 0 {
				t.Fatalf("failed export presigned %v, want nothing", presigned)
			}
		})
	}
}

func TestExportLoansToS3PresignFailureAnswers502(t *testing.T) {
	store := NewMemoryLoanStore()
	seedLoans(t, store)

	storage := newFakeExportStorage()
	storage.presignErr = errStorageUnavailable
	router := exportS3Router(t, store, storage)

	rec := postS3Export(t, router, "/loans/export-s3")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"storage unavailable"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestExportS3RouteIsNotTreatedAsAnId(t *testing.T) {
	store := NewMemoryLoanStore()
	seedLoans(t, store)

	router := exportS3Router(t, store, newFakeExportStorage())

	rec := postS3Export(t, router, "/loans/export-s3")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d for /loans/export-s3, body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("/loans/export-s3 answered an error body: %s", rec.Body.String())
	}
	if key, _ := decodeS3Export(t, rec)["key"].(string); !strings.HasPrefix(key, loansExportPrefix) {
		t.Fatalf("key %q does not start with %q", key, loansExportPrefix)
	}
}

func TestExportS3WithoutStorageAnswers503(t *testing.T) {
	store := NewMemoryLoanStore()
	seedLoans(t, store)

	router := exportS3Router(t, store, nil)

	rec := postS3Export(t, router, "/loans/export-s3")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"storage unavailable"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}

	csv := getPath(t, router, "/loans/export")
	if csv.Code != http.StatusOK {
		t.Fatalf("csv export status %d, body %s", csv.Code, csv.Body.String())
	}
	if got := csv.Header().Get("Content-Type"); got != csvContentType {
		t.Fatalf("Content-Type %q, want %q", got, csvContentType)
	}

	if health := getPath(t, router, "/health"); health.Code != http.StatusOK {
		t.Fatalf("health status %d, body %s", health.Code, health.Body.String())
	}
	if list := getPath(t, router, "/loans"); list.Code != http.StatusOK {
		t.Fatalf("list status %d, body %s", list.Code, list.Body.String())
	}
	if loan := getPath(t, router, "/loans/1"); loan.Code != http.StatusOK {
		t.Fatalf("get status %d, body %s", loan.Code, loan.Body.String())
	}
}

func TestServiceBootsWithoutStorageConfiguration(t *testing.T) {
	for _, name := range []string{
		"S3_ENDPOINT",
		"S3_REGION",
		"S3_BUCKET",
		"S3_ACCESS_KEY",
		"S3_SECRET_KEY",
		"S3_FORCE_PATH_STYLE",
		"S3_PRESIGN_TTL_SECONDS",
	} {
		t.Setenv(name, "")
	}

	store := NewMemoryLoanStore()
	seedLoans(t, store)
	stub := newStubCatalog(t, http.StatusOK, bookOnLoanJSON, http.StatusOK)
	router := newRouterWithStorage(store, NewCatalogClient(stub.server.URL), storageFromEnv())

	for _, path := range []string{"/health", "/loans", "/loans/1", "/loans/export"} {
		if rec := getPath(t, router, path); rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", path, rec.Code, rec.Body.String())
		}
	}

	rec := postS3Export(t, router, "/loans/export-s3")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"storage unavailable"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestStorageConfigFromEnvDefaultsAndOverrides(t *testing.T) {
	env := map[string]string{
		"S3_ENDPOINT":   "http://localhost:9000",
		"S3_BUCKET":     "octiq-uploads",
		"S3_ACCESS_KEY": "access",
		"S3_SECRET_KEY": "secret",
	}
	getenv := func(name string) string { return env[name] }

	cfg, ok := storageConfigFromEnv(getenv)
	if !ok {
		t.Fatalf("storageConfigFromEnv reported disabled for a complete configuration")
	}
	if cfg.Endpoint != "http://localhost:9000" || cfg.Bucket != "octiq-uploads" {
		t.Fatalf("config %+v, want the configured endpoint and bucket", cfg)
	}
	if cfg.Region != "us-east-1" {
		t.Fatalf("region %q, want us-east-1", cfg.Region)
	}
	if cfg.PresignTTL != 900*time.Second {
		t.Fatalf("presign ttl %v, want 900s", cfg.PresignTTL)
	}
	if cfg.ForcePathStyle {
		t.Fatalf("force path style is on by default")
	}

	env["S3_REGION"] = "eu-west-1"
	env["S3_PRESIGN_TTL_SECONDS"] = "60"
	env["S3_FORCE_PATH_STYLE"] = "true"

	cfg, ok = storageConfigFromEnv(getenv)
	if !ok {
		t.Fatalf("storageConfigFromEnv reported disabled after the override")
	}
	if cfg.Region != "eu-west-1" {
		t.Fatalf("region %q, want eu-west-1", cfg.Region)
	}
	if cfg.PresignTTL != 60*time.Second {
		t.Fatalf("presign ttl %v, want 60s", cfg.PresignTTL)
	}
	if !cfg.ForcePathStyle {
		t.Fatalf("force path style is off for S3_FORCE_PATH_STYLE=true")
	}
}

func TestStorageConfigFromEnvNeedsEndpointBucketAndCredentials(t *testing.T) {
	full := map[string]string{
		"S3_ENDPOINT":   "http://localhost:9000",
		"S3_BUCKET":     "octiq-uploads",
		"S3_ACCESS_KEY": "access",
		"S3_SECRET_KEY": "secret",
	}

	for _, missing := range []string{"S3_ENDPOINT", "S3_BUCKET", "S3_ACCESS_KEY", "S3_SECRET_KEY"} {
		t.Run(missing, func(t *testing.T) {
			env := make(map[string]string, len(full))
			for name, value := range full {
				if name != missing {
					env[name] = value
				}
			}

			if _, ok := storageConfigFromEnv(func(name string) string { return env[name] }); ok {
				t.Fatalf("storageConfigFromEnv reported configured without %s", missing)
			}
		})
	}
}
