//go:build integration

package integrationtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/alexandremgo/baguettedb/pkg/databases/adapters/s3store"
	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

const (
	testS3Bucket    = "bdb-test-snapshots"
	testS3AccessKey = "test"
	testS3SecretKey = "test"
)

// rawS3Client builds a plain SDK client against the test container, for setup steps the adapter deliberately does
// not expose (bucket creation, planting foreign objects).
func rawS3Client() *s3.Client {
	return s3.New(s3.Options{
		BaseEndpoint: aws.String(testS3Endpoint),
		Region:       "gra",
		Credentials:  credentials.NewStaticCredentialsProvider(testS3AccessKey, testS3SecretKey, ""),
		UsePathStyle: true,
	})
}

// s3SetupTimeout bounds setupS3Bucket as a whole, and s3SetupAttemptTimeout each of its requests, so a container
// that accepts connections but never answers fails the suite's setup instead of hanging it until go test's timeout.
const (
	s3SetupTimeout        = 60 * time.Second
	s3SetupAttemptTimeout = 5 * time.Second
)

// setupS3Bucket creates the shared test bucket and probes a first write. Retried in a loop: the S3 gateway port
// opens slightly before the master/volume layer is ready to serve bucket creation and writes. Gives up after
// s3SetupTimeout, returning the last attempt's error.
func setupS3Bucket(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s3SetupTimeout)
	defer cancel()

	raw := rawS3Client()
	var lastErr error
	for {
		if lastErr = createBucketAndProbe(ctx, raw); lastErr == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("bucket not ready within %s: %w", s3SetupTimeout, lastErr)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// createBucketAndProbe makes one attempt at creating the shared test bucket and writing a probe object to it, each
// request bounded by s3SetupAttemptTimeout. A bucket that already exists counts as created: an earlier attempt may
// have created it before its probe write failed.
func createBucketAndProbe(ctx context.Context, raw *s3.Client) error {
	ctx, cancel := context.WithTimeout(ctx, s3SetupAttemptTimeout)
	defer cancel()

	_, err := raw.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(testS3Bucket)})
	if err != nil {
		_, owned := errors.AsType[*types.BucketAlreadyOwnedByYou](err)
		_, exists := errors.AsType[*types.BucketAlreadyExists](err)
		if !owned && !exists {
			return fmt.Errorf("creating bucket: %w", err)
		}
	}

	probeKey := "readiness-probe"
	if _, err := raw.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(testS3Bucket),
		Key:    aws.String(probeKey),
		Body:   bytes.NewReader([]byte("probe")),
	}); err != nil {
		return fmt.Errorf("writing the probe object: %w", err)
	}
	if _, err := raw.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(testS3Bucket),
		Key:    aws.String(probeKey),
	}); err != nil {
		return fmt.Errorf("deleting the probe object: %w", err)
	}
	return nil
}

// newSnapshotStoreClient builds an s3store adapter client against the test container. Each test passes a unique
// prefix so tests stay isolated inside the shared bucket.
func newSnapshotStoreClient(t *testing.T, prefix string) *s3store.Client {
	t.Helper()
	c, err := s3store.NewClient(s3store.Config{
		Endpoint:        testS3Endpoint,
		Bucket:          testS3Bucket,
		Prefix:          prefix,
		AccessKeyID:     testS3AccessKey,
		SecretAccessKey: testS3SecretKey,
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatalf("s3store.NewClient: %v", err)
	}
	return c
}

// writeTempSnapshotFile creates a file with the given content and returns its path.
func writeTempSnapshotFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "src.db")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}

// TestS3Store_UploadListLatestDownload_RoundTrip verifies the full snapshot object lifecycle: two uploads at
// different instants, listing newest-first with parsed metadata, latest selection, and a byte-identical download.
func TestS3Store_UploadListLatestDownload_RoundTrip(t *testing.T) {
	c := newSnapshotStoreClient(t, "roundtrip")
	ctx := context.Background()

	older := time.Date(2026, 6, 11, 8, 0, 0, 111000000, time.UTC)
	newer := time.Date(2026, 6, 11, 9, 0, 0, 222000000, time.UTC)

	if _, err := c.UploadSnapshot(ctx, "db-1", writeTempSnapshotFile(t, "older-content"), older); err != nil {
		t.Fatalf("uploading older snapshot: %v", err)
	}
	uploaded, err := c.UploadSnapshot(ctx, "db-1", writeTempSnapshotFile(t, "newer-content"), newer)
	if err != nil {
		t.Fatalf("uploading newer snapshot: %v", err)
	}
	if uploaded.SizeBytes != int64(len("newer-content")) {
		t.Errorf("uploaded SizeBytes = %d, want %d", uploaded.SizeBytes, len("newer-content"))
	}

	snapshots, err := c.ListSnapshots(ctx, "db-1")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("len(snapshots) = %d, want 2", len(snapshots))
	}
	if !snapshots[0].TakenAt.Equal(newer) || !snapshots[1].TakenAt.Equal(older) {
		t.Errorf("snapshots not newest-first: got %v then %v", snapshots[0].TakenAt, snapshots[1].TakenAt)
	}
	if snapshots[0].DatabaseID != "db-1" {
		t.Errorf("DatabaseID = %q, want db-1", snapshots[0].DatabaseID)
	}
	if snapshots[0].SizeBytes != int64(len("newer-content")) {
		t.Errorf("listed SizeBytes = %d, want %d", snapshots[0].SizeBytes, len("newer-content"))
	}

	latest, err := c.LatestSnapshot(ctx, "db-1")
	if err != nil {
		t.Fatalf("LatestSnapshot: %v", err)
	}
	if latest.Key != uploaded.Key {
		t.Errorf("latest.Key = %q, want %q", latest.Key, uploaded.Key)
	}

	destPath := filepath.Join(t.TempDir(), "restored.db")
	if err := c.DownloadSnapshot(ctx, latest.Key, destPath); err != nil {
		t.Fatalf("DownloadSnapshot: %v", err)
	}
	content, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(content) != "newer-content" {
		t.Errorf("downloaded content = %q, want %q", content, "newer-content")
	}
}

// TestS3Store_Isolation verifies snapshots are invisible across config prefixes and across databaseIDs.
func TestS3Store_Isolation(t *testing.T) {
	ctx := context.Background()
	takenAt := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)

	cA := newSnapshotStoreClient(t, "iso-env-a")
	cB := newSnapshotStoreClient(t, "iso-env-b")

	if _, err := cA.UploadSnapshot(ctx, "db-1", writeTempSnapshotFile(t, "content-a"), takenAt); err != nil {
		t.Fatalf("upload in prefix a: %v", err)
	}

	if snapshots, err := cB.ListSnapshots(ctx, "db-1"); err != nil {
		t.Fatalf("listing in prefix b: %v", err)
	} else if len(snapshots) != 0 {
		t.Errorf("prefix b sees %d snapshots from prefix a, want 0", len(snapshots))
	}

	if snapshots, err := cA.ListSnapshots(ctx, "db-2"); err != nil {
		t.Fatalf("listing other database: %v", err)
	} else if len(snapshots) != 0 {
		t.Errorf("db-2 sees %d snapshots from db-1, want 0", len(snapshots))
	}
}

// TestS3Store_ListSkipsForeignObjects verifies objects under a database's prefix that don't follow the snapshot
// key layout are ignored rather than breaking the listing.
func TestS3Store_ListSkipsForeignObjects(t *testing.T) {
	c := newSnapshotStoreClient(t, "foreign")
	ctx := context.Background()
	takenAt := time.Date(2026, 6, 11, 11, 0, 0, 0, time.UTC)

	if _, err := c.UploadSnapshot(ctx, "db-1", writeTempSnapshotFile(t, "real"), takenAt); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if _, err := rawS3Client().PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(testS3Bucket),
		Key:    aws.String("foreign/snapshots/db-1/not-a-timestamp.db"),
		Body:   bytes.NewReader([]byte("junk")),
	}); err != nil {
		t.Fatalf("planting foreign object: %v", err)
	}

	snapshots, err := c.ListSnapshots(ctx, "db-1")
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("len(snapshots) = %d, want 1 (foreign object must be skipped)", len(snapshots))
	}
	if !snapshots[0].TakenAt.Equal(takenAt) {
		t.Errorf("TakenAt = %v, want %v", snapshots[0].TakenAt, takenAt)
	}
}

// TestS3Store_LatestSnapshot_NoneExists verifies LatestSnapshot surfaces ErrNoSnapshots for an unknown database.
func TestS3Store_LatestSnapshot_NoneExists(t *testing.T) {
	c := newSnapshotStoreClient(t, "empty")

	_, err := c.LatestSnapshot(context.Background(), "db-never-backed-up")
	if !errors.Is(err, domain.ErrNoSnapshots) {
		t.Errorf("err = %v, want ErrNoSnapshots", err)
	}
}

// TestS3Store_DownloadSnapshot_MissingObject verifies a vanished object yields ErrSnapshotNotFound and leaves no
// partial file behind.
func TestS3Store_DownloadSnapshot_MissingObject(t *testing.T) {
	c := newSnapshotStoreClient(t, "missing")
	destPath := filepath.Join(t.TempDir(), "restored.db")

	err := c.DownloadSnapshot(context.Background(), "missing/snapshots/db-1/2026-01-01T00:00:00.000000000Z.db",
		destPath)
	if !errors.Is(err, domain.ErrSnapshotNotFound) {
		t.Errorf("err = %v, want ErrSnapshotNotFound", err)
	}
	if _, statErr := os.Stat(destPath); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("expected no partial file at destPath, stat err = %v", statErr)
	}
}

// TestS3Store_DownloadSnapshot_DestExists verifies an existing destination file is refused and left untouched.
func TestS3Store_DownloadSnapshot_DestExists(t *testing.T) {
	c := newSnapshotStoreClient(t, "dest-exists")
	ctx := context.Background()
	takenAt := time.Date(2026, 6, 11, 12, 0, 0, 0, time.UTC)

	uploaded, err := c.UploadSnapshot(ctx, "db-1", writeTempSnapshotFile(t, "content"), takenAt)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	destPath := filepath.Join(t.TempDir(), "restored.db")
	if err := os.WriteFile(destPath, []byte("pre-existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.DownloadSnapshot(ctx, uploaded.Key, destPath); !errors.Is(err, fs.ErrExist) {
		t.Errorf("err = %v, want fs.ErrExist", err)
	}
	content, rerr := os.ReadFile(destPath)
	if rerr != nil || string(content) != "pre-existing" {
		t.Errorf("destination modified: content = %q, err = %v", content, rerr)
	}
}
