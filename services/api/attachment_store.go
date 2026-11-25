package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// attachmentStore keeps the bytes of photos, signatures and files. Every
// file is stored under its SHA-256 hash and is never replaced, so a stored
// attachment cannot be overwritten.
type attachmentStore interface {
	// SpoolDir is where the API writes an upload while it reads and hashes it.
	SpoolDir() string
	// Put stores the spooled file under key, unless a file is already stored
	// there. Because keys are content hashes, that file has the same bytes.
	Put(ctx context.Context, key, spooled string, size int64, contentType string) error
	// Open reads a stored file. It returns an error wrapping fs.ErrNotExist
	// when there is none.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

var attachmentKey = regexp.MustCompile(`^sha256/[0-9a-f]{2}/[0-9a-f]{64}$`)

// attachmentKeyFor returns the storage key for a file's SHA-256 hash.
func attachmentKeyFor(sum string) string { return "sha256/" + sum[:2] + "/" + sum }

// localAttachments stores files in a directory on the API server's disk.
type localAttachments struct{ dir string }

func newLocalAttachments(dir string) (localAttachments, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return localAttachments{}, err
	}
	if err := os.MkdirAll(filepath.Join(abs, ".incoming"), 0o700); err != nil {
		return localAttachments{}, fmt.Errorf("could not create the attachment directory: %w", err)
	}
	return localAttachments{dir: abs}, nil
}

func (s localAttachments) SpoolDir() string { return filepath.Join(s.dir, ".incoming") }

func (s localAttachments) path(key string) (string, error) {
	if !attachmentKey.MatchString(key) {
		return "", fmt.Errorf("invalid attachment key %q", key)
	}
	return filepath.Join(s.dir, filepath.FromSlash(key)), nil
}

func (s localAttachments) Put(_ context.Context, key, spooled string, _ int64, _ string) error {
	final, err := s.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return err
	}
	// A hard link never replaces an existing file: it fails if one is there.
	err = os.Link(spooled, final)
	if errors.Is(err, fs.ErrExist) {
		return nil
	}
	return err
}

func (s localAttachments) Open(_ context.Context, key string) (io.ReadCloser, error) {
	final, err := s.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(final)
}

// s3Attachments stores files in a bucket of an S3-compatible object store,
// addressed by path. It signs requests with AWS Signature Version 4, which
// S3-compatible stores accept, so it needs no extra libraries.
type s3Attachments struct {
	endpoint  *url.URL
	region    string
	bucket    string
	prefix    string
	accessKey string
	secretKey string
	spool     string
	client    *http.Client
	now       func() time.Time
}

type s3Settings struct {
	Endpoint, Region, Bucket, Prefix, AccessKey, SecretKey, SpoolDir string
}

func newS3Attachments(cfg s3Settings) (*s3Attachments, error) {
	endpoint, err := url.Parse(strings.TrimRight(cfg.Endpoint, "/"))
	if err != nil || (endpoint.Scheme != "https" && endpoint.Scheme != "http") || endpoint.Host == "" || endpoint.RawQuery != "" {
		return nil, errors.New("FIMRA_S3_ENDPOINT must be an http or https address, such as https://storage.example.invalid")
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`).MatchString(cfg.Bucket) {
		return nil, errors.New("FIMRA_S3_BUCKET must be a bucket name of small letters, digits, dots and hyphens")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("FIMRA_S3_ACCESS_KEY and FIMRA_S3_SECRET_KEY are required for S3 attachments")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	prefix := strings.Trim(cfg.Prefix, "/")
	if prefix != "" {
		if !regexp.MustCompile(`^[A-Za-z0-9._/-]+$`).MatchString(prefix) {
			return nil, errors.New("FIMRA_S3_PREFIX may use only letters, digits, dots, underscores, hyphens and slashes")
		}
		prefix += "/"
	}
	if cfg.SpoolDir == "" {
		cfg.SpoolDir = os.TempDir()
	}
	if err := os.MkdirAll(cfg.SpoolDir, 0o700); err != nil {
		return nil, err
	}
	return &s3Attachments{endpoint: endpoint, region: cfg.Region, bucket: cfg.Bucket, prefix: prefix,
		accessKey: cfg.AccessKey, secretKey: cfg.SecretKey, spool: cfg.SpoolDir,
		client: &http.Client{Timeout: 2 * time.Minute}, now: time.Now}, nil
}

func (s *s3Attachments) SpoolDir() string { return s.spool }

func (s *s3Attachments) objectURL(key string) (string, error) {
	if !attachmentKey.MatchString(key) {
		return "", fmt.Errorf("invalid attachment key %q", key)
	}
	return s.endpoint.String() + "/" + s.bucket + "/" + s.prefix + key, nil
}

// emptyPayloadHash is the SHA-256 hash of an empty body.
const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func (s *s3Attachments) do(ctx context.Context, method, key string, body *os.File, size int64, payloadHash string, header http.Header) (*http.Response, error) {
	target, err := s.objectURL(key)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = body
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	for name, values := range header {
		req.Header[name] = values
	}
	signS3(req, payloadHash, s.accessKey, s.secretKey, s.region, s.now())
	return s.client.Do(req)
}

func (s *s3Attachments) Put(ctx context.Context, key, spooled string, size int64, contentType string) error {
	res, err := s.do(ctx, http.MethodHead, key, nil, 0, emptyPayloadHash, nil)
	if err != nil {
		return err
	}
	res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
	default:
		return fmt.Errorf("the attachment store answered HTTP %d to a check", res.StatusCode)
	}
	file, err := os.Open(spooled)
	if err != nil {
		return err
	}
	defer file.Close()
	sum := key[strings.LastIndexByte(key, '/')+1:]
	// If-None-Match asks stores that support it to refuse an overwrite.
	res, err = s.do(ctx, http.MethodPut, key, file, size, sum, http.Header{"Content-Type": {contentType}, "If-None-Match": {"*"}})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	switch res.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent, http.StatusPreconditionFailed:
		return nil
	}
	return fmt.Errorf("the attachment store answered HTTP %d to an upload", res.StatusCode)
}

func (s *s3Attachments) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	res, err := s.do(ctx, http.MethodGet, key, nil, 0, emptyPayloadHash, nil)
	if err != nil {
		return nil, err
	}
	switch res.StatusCode {
	case http.StatusOK:
		return res.Body, nil
	case http.StatusNotFound:
		res.Body.Close()
		return nil, fmt.Errorf("attachment %s: %w", key, fs.ErrNotExist)
	}
	res.Body.Close()
	return nil, fmt.Errorf("the attachment store answered HTTP %d to a download", res.StatusCode)
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// signS3 adds an AWS Signature Version 4 to a request for the S3 service.
// It signs the host, every x-amz- header and any Range header.
func signS3(req *http.Request, payloadHash, accessKey, secretKey, region string, now time.Time) {
	stamp := now.UTC().Format("20060102T150405Z")
	day := stamp[:8]
	req.Header.Set("X-Amz-Date", stamp)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	headers := map[string]string{"host": req.URL.Host}
	for name, values := range req.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-") || lower == "range" {
			headers[lower] = strings.TrimSpace(strings.Join(values, ","))
		}
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name + ":" + headers[name] + "\n")
	}
	signed := strings.Join(names, ";")
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{req.Method, path, req.URL.Query().Encode(), canonicalHeaders.String(), signed, payloadHash}, "\n")
	digest := sha256.Sum256([]byte(canonical))
	scope := day + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(digest[:])
	key := hmacSHA256(hmacSHA256(hmacSHA256(hmacSHA256([]byte("AWS4"+secretKey), day), region), "s3"), "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+signature)
}
