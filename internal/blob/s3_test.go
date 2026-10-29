package blob

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// fakeS3 is an S3-compatible stand-in: it stores objects and verifies each
// request's SigV4 signature with its own implementation, so a signing bug
// in the client fails the test rather than passing vacuously.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !verifySigV4(r) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "bad signature")
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.objects[key] = body
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		body, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "NoSuchKey")
			return
		}
		_, _ = w.Write(body)
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	}
}

// verifySigV4 recomputes the signature from the request's own headers.
func verifySigV4(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	parts := strings.SplitN(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ", ", 3)
	if len(parts) != 3 {
		return false
	}
	var scope string
	if _, err := fmt.Sscanf(parts[0], "Credential=testkey/%s", &scope); err != nil {
		return false
	}
	scope = strings.TrimSuffix(scope, "/s3/aws4_request")
	if !strings.HasPrefix(parts[1], "SignedHeaders=") || !strings.HasPrefix(parts[2], "Signature=") {
		return false
	}
	stamp := r.Header.Get("x-amz-date")
	canonical := strings.Join([]string{
		r.Method,
		r.URL.Path,
		"",
		"host:" + r.Host + "\n" +
			"x-amz-content-sha256:" + r.Header.Get("x-amz-content-sha256") + "\n" +
			"x-amz-date:" + stamp + "\n",
		"host;x-amz-content-sha256;x-amz-date",
		"UNSIGNED-PAYLOAD",
	}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", stamp, scope + "/s3/aws4_request", hex.EncodeToString(sum[:]),
	}, "\n")
	mac := hmac.New(sha256.New, []byte("AWS4testsecret"))
	mac.Write([]byte(scope[:8]))
	k := mac.Sum(nil)
	for _, part := range []string{"us-east-1", "s3", "aws4_request", stringToSign} {
		mac = hmac.New(sha256.New, k)
		mac.Write([]byte(part))
		k = mac.Sum(nil)
	}
	return parts[2] == "Signature="+hex.EncodeToString(k)
}

func TestS3Roundtrip(t *testing.T) {
	ctx := context.Background()
	fake := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	c := NewS3(srv.URL, "bucket", "", "testkey", "testsecret", true)

	body := []byte("pixels")
	if err := c.Put(ctx, 3, int64(len(body)), bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	rc, err := c.Open(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("roundtrip got %q", got)
	}
	if err := c.Delete(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Open(ctx, 3); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("open after delete: %v", err)
	}
}

func TestS3ObjectURLs(t *testing.T) {
	vhost := NewS3("s3.example.com", "b", "", "k", "s", false)
	url, host, uri := vhost.object(1)
	if url != "https://b.s3.example.com/blobs/1" || host != "b.s3.example.com" || uri != "/blobs/1" {
		t.Fatalf("virtual-hosted style: %s %s %s", url, host, uri)
	}
	pathed := NewS3("http://minio.lan:9000/", "b", "", "k", "s", true)
	url, host, uri = pathed.object(2)
	if url != "http://minio.lan:9000/b/blobs/2" || host != "minio.lan:9000" || uri != "/b/blobs/2" {
		t.Fatalf("path style: %s %s %s", url, host, uri)
	}
}
