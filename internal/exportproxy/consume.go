package exportproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const consumeMaxRedirects = 5

// ConsumeResult reports how much of a limited download reached VoCat.
type ConsumeResult struct {
	RequestedBytes int64
	PayloadBytes   int64
	Complete       bool
}

// ConsumeDownload reads at most limit response-body bytes from rawURL through
// the marked, interface-bound path and discards them. Connections stay open
// across same-host redirects because every extra TLS handshake is billed.
func ConsumeDownload(ctx context.Context, networkInterface, rawURL string, limit int64) (ConsumeResult, error) {
	networkInterface = strings.TrimSpace(networkInterface)
	if networkInterface == "" {
		return ConsumeResult{}, errors.New("cellular network interface is required")
	}
	if err := platformSupported(); err != nil {
		return ConsumeResult{}, err
	}
	dialer := boundDialer(networkInterface)
	resolver := boundResolver(networkInterface)
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			return dialTarget(ctx, address, &dialer, resolver)
		},
		DisableCompression:    true,
		TLSHandshakeTimeout:   12 * time.Second,
		ResponseHeaderTimeout: 12 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: consumeRedirectPolicy}
	return consumeHTTP(ctx, client, rawURL, limit)
}

func consumeRedirectPolicy(request *http.Request, via []*http.Request) error {
	if len(via) >= consumeMaxRedirects {
		return errors.New("too many redirects")
	}
	if request.URL.Scheme != "https" {
		return errors.New("redirect target must use HTTPS")
	}
	return nil
}

// consumeHTTP asks the server for only the requested range and still enforces
// the limit locally, because a server may ignore Range and send the whole file.
func consumeHTTP(ctx context.Context, client *http.Client, rawURL string, limit int64) (ConsumeResult, error) {
	result := ConsumeResult{RequestedBytes: limit}
	if limit <= 0 {
		return result, errors.New("download limit must be positive")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return result, err
	}
	request.Header.Set("Range", fmt.Sprintf("bytes=0-%d", limit-1))
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "VoCat/1.0")
	response, err := client.Do(request)
	if err != nil {
		return result, fmt.Errorf("request download: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		return result, fmt.Errorf("download returned HTTP %d", response.StatusCode)
	}
	written, err := io.Copy(io.Discard, io.LimitReader(response.Body, limit))
	result.PayloadBytes = written
	if err != nil {
		return result, fmt.Errorf("read download: %w", err)
	}
	if written != limit {
		return result, fmt.Errorf("download ended after %d of %d bytes", written, limit)
	}
	result.Complete = true
	return result, nil
}
