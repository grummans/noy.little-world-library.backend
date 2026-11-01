package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func testConfig(endpoint, public string) Config {
	return Config{
		Endpoint:       endpoint,
		PublicEndpoint: public,
		Region:         "us-east-1",
		Bucket:         "little-world",
		AccessKey:      "test-access-key",
		SecretKey:      "test-secret-key",
	}
}

func TestNewRejectsIncompleteConfig(t *testing.T) {
	cfg := testConfig("http://192.168.1.48:9000", "")
	cfg.SecretKey = ""

	if _, err := New(cfg); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

func TestNewRequiresEndpointScheme(t *testing.T) {
	if _, err := New(testConfig("192.168.1.48:9000", "")); err == nil {
		t.Fatal("endpoint without http:// was accepted")
	}
}

// Presigning is local signing, so it works with fake credentials and no server.
func TestPresignUsesPublicEndpoint(t *testing.T) {
	client, err := New(testConfig("http://minio:9000", "http://192.168.1.48:9000"))
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	ctx := context.Background()
	signed, err := client.PresignGet(ctx, EpubKey(42))
	if err != nil {
		t.Fatalf("presign: %v", err)
	}

	if !strings.HasPrefix(signed, "http://192.168.1.48:9000/little-world/books/42/book.epub?") {
		t.Errorf("signed url = %s, want public host + bucket + key", signed)
	}
	if strings.Contains(signed, "minio:9000") {
		t.Error("signed url leaks the internal endpoint; the signature would not validate")
	}
	for _, param := range []string{"X-Amz-Signature=", "X-Amz-Expires=", "X-Amz-SignedHeaders="} {
		if !strings.Contains(signed, param) {
			t.Errorf("signed url missing %s", param)
		}
	}
}

func TestObjectKeys(t *testing.T) {
	if got := EpubKey(7); got != "books/7/book.epub" {
		t.Errorf("EpubKey = %q", got)
	}

	cover, err := BookCoverKey(7, "image/png")
	if err != nil {
		t.Fatalf("book cover key: %v", err)
	}
	if cover != "books/7/cover.png" {
		t.Errorf("BookCoverKey = %q", cover)
	}

	bundleCover, err := BundleCoverKey(3, "IMAGE/JPEG; charset=binary")
	if err != nil {
		t.Fatalf("bundle cover key: %v", err)
	}
	if bundleCover != "bundles/3/cover.jpg" {
		t.Errorf("BundleCoverKey = %q", bundleCover)
	}

	if _, err := BookCoverKey(7, "application/pdf"); !errors.Is(err, ErrInvalidImage) {
		t.Errorf("pdf cover: err = %v, want ErrInvalidImage", err)
	}
}

func TestValidateUploads(t *testing.T) {
	if err := ValidateEPUB("application/epub+zip", 1<<20); err != nil {
		t.Errorf("valid epub rejected: %v", err)
	}
	if err := ValidateEPUB("application/epub+zip; charset=binary", 1<<20); err != nil {
		t.Errorf("epub with charset parameter rejected: %v", err)
	}
	if err := ValidateEPUB("application/pdf", 1<<20); !errors.Is(err, ErrInvalidEpub) {
		t.Errorf("pdf epub: err = %v", err)
	}
	if err := ValidateEPUB("application/epub+zip", MaxEpubBytes+1); !errors.Is(err, ErrInvalidEpub) {
		t.Errorf("oversized epub: err = %v", err)
	}
	if err := ValidateEPUB("application/epub+zip", 0); !errors.Is(err, ErrInvalidEpub) {
		t.Errorf("empty epub: err = %v", err)
	}

	if err := ValidateCover("image/png", 1024); err != nil {
		t.Errorf("valid cover rejected: %v", err)
	}
	if err := ValidateCover("image/gif", 1024); !errors.Is(err, ErrInvalidImage) {
		t.Errorf("gif cover: err = %v", err)
	}
	if err := ValidateCover("image/png", MaxCoverBytes+1); !errors.Is(err, ErrInvalidImage) {
		t.Errorf("oversized cover: err = %v", err)
	}
}
