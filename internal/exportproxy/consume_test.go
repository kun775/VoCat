package exportproxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func TestConsumeHTTPRequestsOnlyTheLimitAcrossRedirects(t *testing.T) {
	var gotRange string
	mux := http.NewServeMux()
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/asset", http.StatusFound)
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		w.Header().Set("Content-Length", "2048")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 2048))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	result, err := consumeHTTP(context.Background(), server.Client(), server.URL+"/latest", 2048)
	if err != nil {
		t.Fatal(err)
	}
	if gotRange != "bytes=0-2047" {
		t.Fatalf("Range = %q, want bytes=0-2047", gotRange)
	}
	if result != (ConsumeResult{RequestedBytes: 2048, PayloadBytes: 2048, Complete: true}) {
		t.Fatalf("result = %+v", result)
	}
}

func TestConsumeHTTPStopsWhenServerIgnoresRange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 1<<20))
	}))
	defer server.Close()

	result, err := consumeHTTP(context.Background(), server.Client(), server.URL, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if result.PayloadBytes != 1024 || !result.Complete {
		t.Fatalf("result = %+v, want exactly 1024 complete bytes", result)
	}
}

func TestConsumeHTTPReportsPartialAndFailedDownloads(t *testing.T) {
	short := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(10))
		_, _ = w.Write(bytes.Repeat([]byte("x"), 10))
	}))
	defer short.Close()
	result, err := consumeHTTP(context.Background(), short.Client(), short.URL, 1024)
	if err == nil || result.PayloadBytes != 10 || result.Complete {
		t.Fatalf("short result = %+v, err = %v", result, err)
	}

	missing := httptest.NewServer(http.NotFoundHandler())
	defer missing.Close()
	result, err = consumeHTTP(context.Background(), missing.Client(), missing.URL, 1024)
	if err == nil || result.PayloadBytes != 0 {
		t.Fatalf("missing result = %+v, err = %v", result, err)
	}

	if _, err = consumeHTTP(context.Background(), missing.Client(), missing.URL, 0); err == nil {
		t.Fatal("zero limit was accepted")
	}
}

func TestConsumeRedirectPolicy(t *testing.T) {
	secure := &http.Request{URL: &url.URL{Scheme: "https", Host: "example.com"}}
	plain := &http.Request{URL: &url.URL{Scheme: "http", Host: "example.com"}}
	if err := consumeRedirectPolicy(secure, nil); err != nil {
		t.Fatalf("HTTPS redirect rejected: %v", err)
	}
	if err := consumeRedirectPolicy(plain, nil); err == nil {
		t.Fatal("plain HTTP redirect was accepted")
	}
	if err := consumeRedirectPolicy(secure, make([]*http.Request, consumeMaxRedirects)); err == nil {
		t.Fatal("redirect limit was not enforced")
	}
}
