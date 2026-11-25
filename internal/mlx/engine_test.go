package mlx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureFileVerifiesAndRepairsCache(t *testing.T) {
	body := []byte("pinned model asset")
	digest := sha256.Sum256(body)
	asset := modelFile{"model", int64(len(body)), hex.EncodeToString(digest[:])}
	dir := t.TempDir()
	path := filepath.Join(dir, asset.name)

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), len(body)))
	}))
	defer bad.Close()
	if err := ensureFile(context.Background(), bad.Client(), bad.URL+"/", dir, asset); err == nil {
		t.Fatal("accepted an invalid checksum")
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("failed download left files in cache: %v, %v", entries, err)
	}

	if err := os.WriteFile(path, []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(good.Close)
	if err := ensureFile(context.Background(), good.Client(), good.URL+"/", dir, asset); err != nil {
		t.Fatal(err)
	}
	good.Close()
	if err := ensureFile(context.Background(), good.Client(), good.URL+"/", dir, asset); err != nil {
		t.Fatalf("valid cached file required a network connection: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("repaired cache = %q, %v", got, err)
	}
}

func TestVerifyFileRejectsExcessBytes(t *testing.T) {
	body := []byte("verified")
	digest := sha256.Sum256(body)
	asset := modelFile{"model", int64(len(body)), hex.EncodeToString(digest[:])}
	if err := verifyFile(io.Discard, bytes.NewReader(append(body, 'x')), asset); err == nil {
		t.Fatal("accepted extra data")
	}
}
