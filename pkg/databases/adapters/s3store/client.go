// Package s3store stores database snapshot artifacts in an S3-compatible object store. It owns the object layout
// (`[<prefix>/]snapshots/<databaseID>/<RFC3339-ts>.db`) and exposes upload / download / list primitives; deciding
// when to snapshot or restore belongs to the service layer.
package s3store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/alexandremgo/baguettedb/pkg/databases/domain"
)

// snapshotKeyTimeLayout is RFC3339 with fixed-width nanoseconds. The fixed width keeps UTC-formatted keys
// lexicographically ordered by time (RFC3339Nano trims trailing zeros, which breaks that), and the nanosecond
// precision makes key collisions for one database practically impossible.
const snapshotKeyTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// Config bundles the connection settings for an S3-compatible object store.
// All values come from the environment via the central config (never hardcoded credentials).
type Config struct {
	// Endpoint is the S3-compatible endpoint URL. Required — this adapter always targets an explicit endpoint,
	// never the AWS default resolution.
	Endpoint string

	// Region is the S3 region name. Self-hosted endpoints generally ignore it but the SDK requires one;
	// defaults to "gra" when empty.
	Region string

	// Bucket is the bucket holding snapshot objects. Required, and assumed to already exist (provisioned by the
	// deployment, not by the application).
	Bucket string

	// Prefix is an optional key prefix prepended to every object key (e.g. an environment name). Leading and
	// trailing slashes are normalized away.
	Prefix string

	// AccessKeyID and SecretAccessKey are the static credentials for the endpoint. Required.
	AccessKeyID     string
	SecretAccessKey string

	// UsePathStyle switches the client to path-style addressing (`endpoint/bucket/key` instead of
	// `bucket.endpoint/key`). Required by most self-hosted S3 implementations.
	UsePathStyle bool
}

// Client is a snapshot-object store backed by an S3-compatible bucket. Safe for concurrent use.
type Client struct {
	s3c    *s3.Client
	bucket string
	prefix string
}

// NewClient validates cfg and builds the S3 client. It performs no network call — a bad endpoint or credentials
// surface on the first operation.
func NewClient(cfg Config) (*Client, error) {
	if cfg.Endpoint == "" {
		return nil, errors.New("s3store: Endpoint is required")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("s3store: Bucket is required")
	}
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("s3store: AccessKeyID and SecretAccessKey are required")
	}
	region := cfg.Region
	if region == "" {
		region = "gra"
	}

	s3c := s3.New(s3.Options{
		BaseEndpoint: aws.String(cfg.Endpoint),
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		UsePathStyle: cfg.UsePathStyle,
	})

	return &Client{
		s3c:    s3c,
		bucket: cfg.Bucket,
		prefix: strings.Trim(cfg.Prefix, "/"),
	}, nil
}

// UploadSnapshot uploads the snapshot file at srcPath for databaseID, keyed by takenAt, and returns the stored
// object's info. A single PutObject is plenty at snapshot-file sizes (single-digit MB; S3's single-part cap is
// 5 GB) and the seekable file body lets the SDK retry transparently.
func (c *Client) UploadSnapshot(
	ctx context.Context, databaseID string, srcPath string, takenAt time.Time,
) (domain.SnapshotInfo, error) {
	if databaseID == "" {
		return domain.SnapshotInfo{}, errors.New("s3store: databaseID is required")
	}

	f, err := os.Open(srcPath)
	if err != nil {
		return domain.SnapshotInfo{}, fmt.Errorf("opening snapshot file: %w", err)
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return domain.SnapshotInfo{}, fmt.Errorf("stating snapshot file: %w", err)
	}

	key := c.snapshotKey(databaseID, takenAt)
	if _, err := c.s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		Body:          f,
		ContentLength: aws.Int64(stat.Size()),
	}); err != nil {
		return domain.SnapshotInfo{}, fmt.Errorf("uploading snapshot %q: %w", key, err)
	}

	return domain.SnapshotInfo{
		Key:        key,
		DatabaseID: databaseID,
		TakenAt:    takenAt.UTC(),
		SizeBytes:  stat.Size(),
	}, nil
}

// DownloadSnapshot downloads the object at key to destPath. destPath must not exist yet; on failure the partial
// file is removed.
func (c *Client) DownloadSnapshot(ctx context.Context, key string, destPath string) error {
	f, err := os.OpenFile(destPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("creating download destination: %w", err)
	}

	dlErr := func() error {
		resp, err := c.s3c.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(c.bucket),
			Key:    aws.String(key),
		})
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		_, err = io.Copy(f, resp.Body)
		return err
	}()
	closeErr := f.Close()

	if dlErr != nil {
		if rmErr := os.Remove(destPath); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
			dlErr = errors.Join(dlErr, fmt.Errorf("removing partial download: %w", rmErr))
		}
		if isNotFound(dlErr) {
			return fmt.Errorf("%w: %q", domain.ErrSnapshotNotFound, key)
		}
		return fmt.Errorf("downloading snapshot %q: %w", key, dlErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing downloaded snapshot %q: %w", destPath, closeErr)
	}
	return nil
}

// ListSnapshots returns the stored snapshots for databaseID, newest first. Objects under the database's prefix
// whose name does not parse as a snapshot timestamp are skipped.
func (c *Client) ListSnapshots(ctx context.Context, databaseID string) ([]domain.SnapshotInfo, error) {
	if databaseID == "" {
		return nil, errors.New("s3store: databaseID is required")
	}

	keyPrefix := c.databasePrefix(databaseID)
	var out []domain.SnapshotInfo

	paginator := s3.NewListObjectsV2Paginator(c.s3c, &s3.ListObjectsV2Input{
		Bucket: aws.String(c.bucket),
		Prefix: aws.String(keyPrefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing snapshots under %q: %w", keyPrefix, err)
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			takenAt, ok := parseSnapshotKeyTime(key, keyPrefix)
			if !ok {
				continue
			}
			out = append(out, domain.SnapshotInfo{
				Key:        key,
				DatabaseID: databaseID,
				TakenAt:    takenAt,
				SizeBytes:  aws.ToInt64(obj.Size),
			})
		}
	}

	slices.SortFunc(out, func(a, b domain.SnapshotInfo) int { return b.TakenAt.Compare(a.TakenAt) })
	return out, nil
}

// LatestSnapshot returns the most recent snapshot for databaseID, or domain.ErrNoSnapshots when none exist.
func (c *Client) LatestSnapshot(ctx context.Context, databaseID string) (domain.SnapshotInfo, error) {
	snapshots, err := c.ListSnapshots(ctx, databaseID)
	if err != nil {
		return domain.SnapshotInfo{}, err
	}
	if len(snapshots) == 0 {
		return domain.SnapshotInfo{}, fmt.Errorf("%w: %q", domain.ErrNoSnapshots, databaseID)
	}
	return snapshots[0], nil
}

// snapshotKey builds the object key for a snapshot of databaseID taken at takenAt.
func (c *Client) snapshotKey(databaseID string, takenAt time.Time) string {
	return c.databasePrefix(databaseID) + takenAt.UTC().Format(snapshotKeyTimeLayout) + ".db"
}

// databasePrefix returns the key prefix holding databaseID's snapshots, with a trailing slash.
func (c *Client) databasePrefix(databaseID string) string {
	return path.Join(c.prefix, "snapshots", databaseID) + "/"
}

// parseSnapshotKeyTime extracts the snapshot timestamp from an object key. Reports false for keys that do not
// follow the snapshot layout (foreign objects under the same prefix).
func parseSnapshotKeyTime(key, keyPrefix string) (time.Time, bool) {
	name, found := strings.CutPrefix(key, keyPrefix)
	if !found {
		return time.Time{}, false
	}
	name, found = strings.CutSuffix(name, ".db")
	if !found || strings.Contains(name, "/") {
		return time.Time{}, false
	}
	t, err := time.Parse(snapshotKeyTimeLayout, name)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// isNotFound reports whether err is the store's object-not-found response. Matches both the SDK's typed NoSuchKey
// error and the generic API error code, since S3-compatible implementations vary in how faithfully they shape it.
func isNotFound(err error) bool {
	if _, ok := errors.AsType[*types.NoSuchKey](err); ok {
		return true
	}
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok {
		return apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound"
	}
	return false
}
