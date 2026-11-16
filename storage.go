package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const (
	// defaultS3Region is the signing region used when S3_REGION is unset.
	defaultS3Region = "us-east-1"
	// defaultPresignTTLSeconds is the presigned GET lifetime, in seconds, used
	// when S3_PRESIGN_TTL_SECONDS is unset.
	defaultPresignTTLSeconds = 900
)

// storageConfig is the object store configuration read from the S3_* variables.
type storageConfig struct {
	Endpoint       string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
	ForcePathStyle bool
	PresignTTL     time.Duration
}

// storageConfigFromEnv reads the storage configuration through getenv. ok is
// false when the endpoint, bucket or credentials are missing, which leaves
// storage disabled instead of failing the boot.
func storageConfigFromEnv(getenv func(string) string) (storageConfig, bool) {
	cfg := storageConfig{
		Endpoint:       strings.TrimSpace(getenv("S3_ENDPOINT")),
		Region:         strings.TrimSpace(getenv("S3_REGION")),
		Bucket:         strings.TrimSpace(getenv("S3_BUCKET")),
		AccessKey:      strings.TrimSpace(getenv("S3_ACCESS_KEY")),
		SecretKey:      strings.TrimSpace(getenv("S3_SECRET_KEY")),
		ForcePathStyle: strings.EqualFold(strings.TrimSpace(getenv("S3_FORCE_PATH_STYLE")), "true"),
		PresignTTL:     defaultPresignTTLSeconds * time.Second,
	}
	if cfg.Region == "" {
		cfg.Region = defaultS3Region
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(getenv("S3_PRESIGN_TTL_SECONDS"))); err == nil && seconds > 0 {
		cfg.PresignTTL = time.Duration(seconds) * time.Second
	}
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return storageConfig{}, false
	}

	return cfg, true
}

var (
	// errStorageNotConfigured is answered as 503: the S3_* variables are unset.
	errStorageNotConfigured = errors.New("object storage not configured")
	// errStorageUnavailable is answered as 502: the endpoint could not serve
	// the request.
	errStorageUnavailable = errors.New("object storage unavailable")
)

// objectStorage is the slice of the object store the loan service needs: the
// export writes one object and hands back a presigned GET for it.
type objectStorage interface {
	Put(ctx context.Context, key string, data []byte, contentType string) error
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// minioStorage is the minio-go backed objectStorage.
type minioStorage struct {
	client *minio.Client
	bucket string
	ttl    time.Duration
}

// newMinioStorage builds the client from cfg. It never contacts the endpoint,
// so an unreachable endpoint only fails when an object is put or presigned.
func newMinioStorage(cfg storageConfig) (*minioStorage, error) {
	host, secure := endpointHost(cfg.Endpoint)

	lookup := minio.BucketLookupAuto
	if cfg.ForcePathStyle {
		lookup = minio.BucketLookupPath
	}

	client, err := minio.New(host, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       secure,
		Region:       cfg.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, fmt.Errorf("building storage client for %s: %w", host, err)
	}

	return &minioStorage{client: client, bucket: cfg.Bucket, ttl: cfg.PresignTTL}, nil
}

// endpointHost strips the scheme from endpoint and reports whether TLS is used.
func endpointHost(endpoint string) (string, bool) {
	trimmed := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return trimmed, true
	}

	return parsed.Host, parsed.Scheme != "http"
}

// Put writes data under key with contentType. The bucket stays private: no ACL
// is applied, so only a presigned URL exposes the object.
func (s *minioStorage) Put(ctx context.Context, key string, data []byte, contentType string) error {
	options := minio.PutObjectOptions{ContentType: contentType}
	if _, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), options); err != nil {
		return fmt.Errorf("%w: putting %s: %w", errStorageUnavailable, key, err)
	}

	return nil
}

// PresignGet returns a presigned GET for key that stays valid for ttl.
func (s *minioStorage) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	presigned, err := s.client.PresignedGetObject(ctx, s.bucket, key, ttl, nil)
	if err != nil {
		return "", fmt.Errorf("%w: presigning %s: %w", errStorageUnavailable, key, err)
	}

	return presigned.String(), nil
}

// presignTTL returns the lifetime the client signs presigned GETs with.
func (s *minioStorage) presignTTL() time.Duration {
	return s.ttl
}

// presignTTLer is implemented by a storage that carries its configured
// presigned GET lifetime.
type presignTTLer interface {
	presignTTL() time.Duration
}

// presignTTLFor returns the lifetime to presign with, falling back to the
// documented default when storage does not carry one.
func presignTTLFor(storage objectStorage) time.Duration {
	if provider, ok := storage.(presignTTLer); ok {
		if ttl := provider.presignTTL(); ttl > 0 {
			return ttl
		}
	}

	return defaultPresignTTLSeconds * time.Second
}

// writeStorageFailure answers a storage failure with the documented body and
// reports whether it handled err.
func writeStorageFailure(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errStorageNotConfigured):
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "storage unavailable"})
		return true
	case errors.Is(err, errStorageUnavailable):
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "storage unavailable"})
		return true
	}

	return false
}
