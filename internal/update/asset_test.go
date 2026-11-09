package update

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestAssetNamesFor(t *testing.T) {
	tests := []struct {
		goos   string
		goarch string
		want   []string
	}{
		{"linux", "amd64", []string{"vocat-linux-amd64"}},
		{"linux", "386", []string{"vocat-linux-386"}},
		{"linux", "arm64", []string{"vocat-linux-arm64", "vocat-linux-aarch64"}},
		{"linux", "arm", []string{"vocat-linux-armv7", "vocat-linux-arm"}},
	}
	for _, item := range tests {
		if got := assetNamesFor(item.goos, item.goarch); !reflect.DeepEqual(got, item.want) {
			t.Errorf("assetNamesFor(%q, %q) = %#v, want %#v", item.goos, item.goarch, got, item.want)
		}
	}
}

func TestLatestAssetURL(t *testing.T) {
	got, err := LatestAssetURL(" owner/repo ", "linux", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://github.com/owner/repo/releases/latest/download/vocat-linux-arm64"
	if got != want {
		t.Fatalf("LatestAssetURL() = %q, want %q", got, want)
	}
	for _, repo := range []string{"", "owner", "owner/repo/extra"} {
		if _, err := LatestAssetURL(repo, "linux", "amd64"); err == nil {
			t.Errorf("LatestAssetURL(%q) succeeded", repo)
		}
	}
}

func TestDownloadAssetWithProgressVerifiesPublishedSize(t *testing.T) {
	payload := bytes.Repeat([]byte("vocat"), 4096)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	var destination bytes.Buffer
	asset := &Asset{Name: "vocat-test", BrowserDownloadURL: server.URL, Size: int64(len(payload))}
	if err := downloadAssetWithProgress(context.Background(), logger, asset, "", &destination); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(destination.Bytes(), payload) {
		t.Fatal("downloaded asset content differs")
	}

	asset.Size++
	if err := downloadAssetWithProgress(context.Background(), logger, asset, "", io.Discard); err == nil {
		t.Fatal("download with a mismatched published size succeeded")
	}
}
