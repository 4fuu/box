package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/4fuu/box/internal/blob"
)

// postBlob uploads one blob over the event API and returns its id.
func postBlob(t *testing.T, addr, token, ctype string, body []byte) int64 {
	t.Helper()
	req, cancel := reqCtx(t, http.MethodPost, addr, "/api/blobs", token, body)
	defer cancel()
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	status, raw := doReq(t, req)
	if status != http.StatusCreated {
		t.Fatalf("blob post status %d: %s", status, raw)
	}
	var made struct {
		ID   int64 `json:"id"`
		Size int64 `json:"size"`
	}
	if err := json.Unmarshal([]byte(raw), &made); err != nil {
		t.Fatal(err)
	}
	if made.Size != int64(len(body)) {
		t.Fatalf("stored size %d, sent %d", made.Size, len(body))
	}
	return made.ID
}

func TestBlobAPI(t *testing.T) {
	addr, token, _ := eventServer(t)

	req, cancel := reqCtx(t, http.MethodPost, addr, "/api/blobs", "", []byte("x"))
	defer cancel()
	if status, _ := doReq(t, req); status != http.StatusUnauthorized {
		t.Fatalf("no token: %d", status)
	}

	id := postBlob(t, addr, token, "image/png", []byte("hello"))

	req, cancel = reqCtx(t, http.MethodGet, addr, "/api/blobs/"+strconv.FormatInt(id, 10), token, nil)
	defer cancel()
	req.Header.Set("Accept", "*/*")
	status, raw := doReq(t, req)
	if status != http.StatusOK || raw != "hello" {
		t.Fatalf("get status %d body %q", status, raw)
	}

	req, cancel = reqCtx(t, http.MethodGet, addr, "/api/blobs/999", token, nil)
	defer cancel()
	if status, body := doReq(t, req); status != http.StatusNotFound || !strings.Contains(body, "no such blob") {
		t.Fatalf("unknown blob: %d %s", status, body)
	}

	over := bytes.Repeat([]byte("x"), blob.MaxSize+1)
	req, cancel = reqCtx(t, http.MethodPost, addr, "/api/blobs", token, over)
	defer cancel()
	if status, body := doReq(t, req); status != http.StatusRequestEntityTooLarge || !strings.Contains(body, "64 MiB") {
		t.Fatalf("oversized blob: %d %s", status, body)
	}
}
