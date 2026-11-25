package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func spoolFile(t *testing.T, dir, body string) (path, key string) {
	t.Helper()
	f, err := os.CreateTemp(dir, "upload-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(body); err != nil {
		t.Fatal(err)
	}
	f.Close()
	sum := sha256.Sum256([]byte(body))
	return f.Name(), attachmentKeyFor(hex.EncodeToString(sum[:]))
}

// checkWriteOnce stores one file twice and checks that the stored bytes
// never change and a missing key reads as not found.
func checkWriteOnce(t *testing.T, store attachmentStore) {
	t.Helper()
	ctx := context.Background()
	first, key := spoolFile(t, store.SpoolDir(), "synthetic photo bytes")
	defer os.Remove(first)
	if err := store.Put(ctx, key, first, 21, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	// A second Put under the same key, even with other bytes, keeps the first file.
	other, _ := spoolFile(t, store.SpoolDir(), "something else entirely")
	defer os.Remove(other)
	if err := store.Put(ctx, key, other, 23, "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	file, err := store.Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(file)
	file.Close()
	if string(data) != "synthetic photo bytes" {
		t.Fatalf("stored file changed to %q", data)
	}
	_, err = store.Open(ctx, attachmentKeyFor(strings.Repeat("ab", 32)))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	if _, err := store.Open(ctx, "../outside"); err == nil {
		t.Fatal("a key outside the store was accepted")
	}
}

func TestLocalAttachmentsWriteOnce(t *testing.T) {
	store, err := newLocalAttachments(filepath.Join(t.TempDir(), "attachments"))
	if err != nil {
		t.Fatal(err)
	}
	checkWriteOnce(t, store)
}

// The example from the AWS Signature Version 4 documentation for S3:
// GET /test.txt with a Range header, signed on 24 May 2013.
func TestSignS3Example(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	signS3(req, emptyPayloadHash, "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "us-east-1",
		time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, " +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// fakeS3 is a stand-in for an S3-compatible store. It refuses unsigned
// requests and overwrites, like a store that supports If-None-Match.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=test-key/") || r.Header.Get("X-Amz-Content-Sha256") == "" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body, found := f.objects[r.URL.Path]
	switch r.Method {
	case http.MethodHead, http.MethodGet:
		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	case http.MethodPut:
		f.puts++
		if found && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		data, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != r.Header.Get("X-Amz-Content-Sha256") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.objects[r.URL.Path] = data
	}
}

func TestS3AttachmentsWithFake(t *testing.T) {
	fake := &fakeS3{objects: map[string][]byte{}}
	server := httptest.NewServer(fake)
	defer server.Close()
	store, err := newS3Attachments(s3Settings{Endpoint: server.URL, Bucket: "demo-attachments", Prefix: "/stack-a/",
		AccessKey: "test-key", SecretKey: "test-secret", SpoolDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	checkWriteOnce(t, store)
	if fake.puts != 1 {
		t.Fatalf("the store uploaded %d times, want once", fake.puts)
	}
	for path := range fake.objects {
		if !strings.HasPrefix(path, "/demo-attachments/stack-a/sha256/") {
			t.Fatalf("object stored at %s", path)
		}
	}
	for _, bad := range []s3Settings{
		{Endpoint: "ftp://x", Bucket: "ok-bucket", AccessKey: "a", SecretKey: "b"},
		{Endpoint: server.URL, Bucket: "Bad_Bucket", AccessKey: "a", SecretKey: "b"},
		{Endpoint: server.URL, Bucket: "ok-bucket"},
	} {
		if _, err := newS3Attachments(bad); err == nil {
			t.Fatalf("settings accepted: %+v", bad)
		}
	}
}

// TestS3AttachmentsIntegration runs the same checks against a real
// S3-compatible server, when FIMRA_TEST_S3_ENDPOINT names one. Make target
// attachments-s3-test starts a small one for this test only.
func TestS3AttachmentsIntegration(t *testing.T) {
	endpoint := os.Getenv("FIMRA_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("Set FIMRA_TEST_S3_ENDPOINT, or run make attachments-s3-test.")
	}
	cfg := s3Settings{Endpoint: endpoint, Bucket: os.Getenv("FIMRA_TEST_S3_BUCKET"), Prefix: "test",
		AccessKey: os.Getenv("FIMRA_TEST_S3_ACCESS_KEY"), SecretKey: os.Getenv("FIMRA_TEST_S3_SECRET_KEY"), SpoolDir: t.TempDir()}
	store, err := newS3Attachments(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Create the bucket. A store that already has it answers with an error, which is fine.
	req, _ := http.NewRequest(http.MethodPut, store.endpoint.String()+"/"+cfg.Bucket, nil)
	signS3(req, emptyPayloadHash, cfg.AccessKey, cfg.SecretKey, store.region, time.Now())
	if res, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	} else {
		res.Body.Close()
	}
	checkWriteOnce(t, store)
	// A request signed with the wrong secret is refused, so the signature is checked.
	wrong := *store
	wrong.secretKey = "not-the-secret"
	if _, err := wrong.Open(context.Background(), attachmentKeyFor(strings.Repeat("cd", 32))); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a wrongly signed request got %v", err)
	}
}
