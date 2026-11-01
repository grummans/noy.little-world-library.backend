// Package storage wraps the S3-compatible object store (MinIO). The database only
// ever stores bucket-relative object keys; URLs are minted here on demand.
package storage

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const (
	EpubContentType = "application/epub+zip"

	MaxEpubBytes  = 50 << 20
	MaxCoverBytes = 5 << 20

	// PresignTTL is how long a signed object URL stays valid.
	PresignTTL = 15 * time.Minute
)

var (
	ErrInvalidImage  = errors.New("cover must be a JPEG or PNG image")
	ErrInvalidEpub   = errors.New("file must be an EPUB under 50 MB")
	ErrNotConfigured = errors.New("object storage is not configured")
)

// coverExtensions maps an accepted image content type to the key extension.
var coverExtensions = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
}

type Config struct {
	// Endpoint is what the API dials, e.g. http://minio:9000.
	Endpoint string
	// PublicEndpoint is the host baked into presigned URLs, because the S3
	// signature covers the Host header the browser will use.
	PublicEndpoint string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
}

func ConfigFromEnv() Config {
	endpoint := os.Getenv("S3_ENDPOINT")
	public := os.Getenv("S3_PUBLIC_ENDPOINT")

	if public == "" {
		public = endpoint
	}
	return Config{
		Endpoint:       endpoint,
		PublicEndpoint: public,
		Region:         defaultString(os.Getenv("S3_REGION"), "us-east-1"),
		Bucket:         os.Getenv("S3_BUCKET"),
		AccessKey:      os.Getenv("S3_ACCESS_KEY"),
		SecretKey:      os.Getenv("S3_SECRET_KEY"),
	}
}

func (c Config) Configured() bool {
	return c.Endpoint != "" && c.Bucket != "" && c.AccessKey != "" && c.SecretKey != ""
}

type Client struct {
	internal *minio.Client
	public   *minio.Client
	bucket   string
}

func New(cfg Config) (*Client, error) {
	if !cfg.Configured() {
		return nil, ErrNotConfigured
	}

	internal, err := newMinio(cfg, cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("internal s3 client: %w", err)
	}
	external, err := newMinio(cfg, cfg.PublicEndpoint)
	if err != nil {
		return nil, fmt.Errorf("public s3 client: %w", err)
	}
	return &Client{internal: internal, public: external, bucket: cfg.Bucket}, nil
}

func newMinio(cfg Config, endpoint string) (*minio.Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint %q: %w", endpoint, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("endpoint %q must start with http:// or https://", endpoint)
	}

	return minio.New(u.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: u.Scheme == "https",
		Region: cfg.Region,
	})
}

func (c *Client) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := c.internal.PutObject(ctx, c.bucket, key, r, size,
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

func (c *Client) Delete(ctx context.Context, key string) error {
	if err := c.internal.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

// PresignGet signs a time-limited URL against the public endpoint.
func (c *Client) PresignGet(ctx context.Context, key string) (string, error) {
	u, err := c.public.PresignedGetObject(ctx, c.bucket, key, PresignTTL, nil)
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, err)
	}
	return u.String(), nil
}

// Check reports whether the bucket is reachable; used by the health probe.
func (c *Client) Check(ctx context.Context) error {
	exists, err := c.internal.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("check bucket %s: %w", c.bucket, err)
	}
	if !exists {
		return fmt.Errorf("bucket %s does not exist", c.bucket)
	}
	return nil
}

// Object keys are fixed per book so replacing an EPUB overwrites it in place and
// buyers always read the current version.
func EpubKey(bookID int64) string {
	return fmt.Sprintf("books/%d/book.epub", bookID)
}

func BookCoverKey(bookID int64, contentType string) (string, error) {
	return coverKey("books", bookID, contentType)
}

func BundleCoverKey(bundleID int64, contentType string) (string, error) {
	return coverKey("bundles", bundleID, contentType)
}

func coverKey(prefix string, id int64, contentType string) (string, error) {
	ext, ok := coverExtensions[normalizeContentType(contentType)]
	if !ok {
		return "", ErrInvalidImage
	}
	return fmt.Sprintf("%s/%d/cover%s", prefix, id, ext), nil
}

// ValidateEPUB checks what the browser claims about the file; the caller still has
// to verify the EPUB magic bytes while streaming.
func ValidateEPUB(contentType string, size int64) error {
	if normalizeContentType(contentType) != EpubContentType || size <= 0 || size > MaxEpubBytes {
		return ErrInvalidEpub
	}
	return nil
}

func ValidateCover(contentType string, size int64) error {
	if _, ok := coverExtensions[normalizeContentType(contentType)]; !ok {
		return ErrInvalidImage
	}
	if size <= 0 || size > MaxCoverBytes {
		return ErrInvalidImage
	}
	return nil
}

// Browsers often send "application/epub+zip; charset=binary" or or fall back to
// octet-stream, so compare only the media type.
func normalizeContentType(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return strings.ToLower(strings.TrimSpace(contentType))
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// Object is what callers need to inspect a stored file without downloading it.
type Object interface {
	io.ReaderAt
	io.Closer
}

// Open exposes a stored object for validation; it is not used to serve bytes.
func (c *Client) Open(ctx context.Context, key string) (Object, error) {
	obj, err := c.internal.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", key, err)
	}
	return obj, nil
}

// Promote copies a validated staging object onto its final key, then drops the
// staging copy. Once the copy succeeds the file IS at the final key, so a failed
// staging cleanup must not be reported as a failed promote — that would make the
// caller roll back a change that already happened. Leftover staging objects are
// handled by cleanup, not by error propagation.
func (c *Client) Promote(ctx context.Context, fromKey, toKey string) error {
	_, err := c.internal.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: c.bucket, Object: toKey},
		minio.CopySrcOptions{Bucket: c.bucket, Object: fromKey},
	)
	if err != nil {
		return fmt.Errorf("promote %s to %s: %w", fromKey, toKey, err)
	}
	_ = c.Delete(ctx, fromKey)
	return nil
}

// StagingKey keeps an unvalidated upload away from any real book key.
func StagingKey(token string) string {
	return "uploads/" + token
}

const maxEpubEntries = 2000

// ValidateEPUBArchive enforces what the EPUB spec requires and readers rely on:
// "mimetype" is the FIRST entry, stored uncompressed, with the exact media type,
// and META-INF/container.xml exists. Magic bytes alone only prove it is a ZIP.
func ValidateEPUBArchive(r io.ReaderAt, size int64) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return ErrInvalidEpub
	}
	if len(zr.File) == 0 || len(zr.File) > maxEpubEntries {
		return ErrInvalidEpub
	}

	mimetype := zr.File[0]
	if mimetype.Name != "mimetype" || mimetype.Method != zip.Store {
		return ErrInvalidEpub
	}

	rc, err := mimetype.Open()
	if err != nil {
		return ErrInvalidEpub
	}
	declared, err := io.ReadAll(io.LimitReader(rc, 64))
	rc.Close()
	if err != nil || string(declared) != EpubContentType {
		return ErrInvalidEpub
	}

	for _, f := range zr.File {
		if f.Name == "META-INF/container.xml" {
			return nil
		}
	}
	return ErrInvalidEpub
}

const (
	maxCoverEdge   = 8000
	maxCoverPixels = 16 << 20
)

var coverFormats = map[string]string{
	"image/jpeg": "jpeg",
	"image/png":  "png",
}

// ValidateCoverImage decodes the header and requires the decoded format to match the
// type the request declared, so a PNG renamed to .jpg cannot claim a wrong extension.
func ValidateCoverImage(r io.ReaderAt, size int64, contentType string) error {
	cfg, format, err := image.DecodeConfig(io.NewSectionReader(r, 0, size))
	if err != nil {
		return ErrInvalidImage
	}
	if want, ok := coverFormats[normalizeContentType(contentType)]; !ok || want != format {
		return ErrInvalidImage
	}
	if cfg.Width < 32 || cfg.Height < 32 {
		return ErrInvalidImage
	}
	if cfg.Width > maxCoverEdge || cfg.Height > maxCoverEdge || cfg.Width*cfg.Height > maxCoverPixels {
		return ErrInvalidImage
	}
	return nil
}

// Stat returns the stored object's size, which zip.NewReader needs.
func (c *Client) Stat(ctx context.Context, key string) (int64, error) {
	info, err := c.internal.StatObject(ctx, c.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", key, err)
	}
	return info.Size, nil
}
