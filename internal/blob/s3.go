package blob

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// S3 speaks the S3 REST API with SigV4, which is what any compatible
// service accepts: S3, OSS, R2, MinIO, B2. One object per blob, keyed
// blobs/<id> in the configured bucket. Payloads are sent UNSIGNED-PAYLOAD
// over HTTPS, so bytes stream without a signing pass.
type S3 struct {
	scheme    string // https, or http when the endpoint says so
	endpoint  string // host or host:port, no scheme
	bucket    string
	region    string
	key       string
	secret    string
	pathStyle bool
	hc        *http.Client
}

// NewS3 returns an S3 backend. endpoint is host or host:port, https by
// default; an explicit http:// endpoint reaches a service on the LAN.
// region defaults to us-east-1. pathStyle addresses MinIO and on-prem
// services; the default is virtual-hosted style.
func NewS3(endpoint, bucket, region, key, secret string, pathStyle bool) *S3 {
	scheme := "https"
	if raw, ok := strings.CutPrefix(endpoint, "http://"); ok {
		scheme, endpoint = "http", raw
	} else {
		endpoint = strings.TrimPrefix(endpoint, "https://")
	}
	endpoint = strings.TrimSuffix(endpoint, "/")
	if region == "" {
		region = "us-east-1"
	}
	return &S3{
		scheme: scheme, endpoint: endpoint, bucket: bucket, region: region,
		key: key, secret: secret, pathStyle: pathStyle,
		hc: &http.Client{},
	}
}

func (c *S3) Name() string { return "s3" }

func (c *S3) object(id int64) (url, host, uri string) {
	uri = "/" + c.bucket + "/blobs/" + strconv.FormatInt(id, 10)
	host = c.endpoint
	if !c.pathStyle {
		host = c.bucket + "." + c.endpoint
		uri = "/blobs/" + strconv.FormatInt(id, 10)
	}
	return c.scheme + "://" + host + uri, host, uri
}

// request builds one signed request. body may be nil; size is the body's
// length when body is not nil.
func (c *S3) request(ctx context.Context, method string, id int64, body io.Reader, size int64) (*http.Request, error) {
	url, host, uri := c.object(id)
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	now := time.Now().UTC()
	date, stamp := now.Format("20060102"), now.Format("20060102T150405Z")
	req.Header.Set("x-amz-date", stamp)
	req.Header.Set("x-amz-content-sha256", "UNSIGNED-PAYLOAD")

	// Canonical request over the three signed headers, in sorted order.
	canonical := strings.Join([]string{
		method,
		uri,
		"", // no query string
		"host:" + host + "\n" +
			"x-amz-content-sha256:UNSIGNED-PAYLOAD\n" +
			"x-amz-date:" + stamp + "\n",
		"host;x-amz-content-sha256;x-amz-date",
		"UNSIGNED-PAYLOAD",
	}, "\n")
	scope := date + "/" + c.region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		stamp,
		scope,
		hex.EncodeToString(sum[:]),
	}, "\n")

	mac := hmac.New(sha256.New, []byte("AWS4"+c.secret))
	mac.Write([]byte(date))
	k := mac.Sum(nil)
	for _, part := range []string{c.region, "s3", "aws4_request", stringToSign} {
		mac = hmac.New(sha256.New, k)
		mac.Write([]byte(part))
		k = mac.Sum(nil)
	}
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=%s",
		c.key, scope, hex.EncodeToString(k)))
	return req, nil
}

// do sends one request and fails on any non-2xx answer.
func (c *S3) do(req *http.Request) (*http.Response, error) {
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	err = fmt.Errorf("s3 %s %s: %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(msg)))
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.Join(err, os.ErrNotExist)
	}
	return nil, err
}

func (c *S3) Put(ctx context.Context, id int64, size int64, body io.Reader) error {
	req, err := c.request(ctx, http.MethodPut, id, body, size)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (c *S3) Open(ctx context.Context, id int64) (io.ReadCloser, error) {
	req, err := c.request(ctx, http.MethodGet, id, nil, 0)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *S3) Delete(ctx context.Context, id int64) error {
	req, err := c.request(ctx, http.MethodDelete, id, nil, 0)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if errors.Is(err, os.ErrNotExist) {
		return nil // retention only ever moves forward
	}
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
