package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vocat/internal/developer"
	"vocat/internal/device"
	"vocat/internal/exportproxy"
	"vocat/internal/store"
)

var connectedConsumeDevice = store.Device{ID: "ec20", Interface: "wwan-test", NetworkEnabled: true}

func newDataConsumeTestServer(t *testing.T) *Server {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	enabled, err := json.Marshal(map[string]bool{"enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertAppSetting(ctx, store.AppSetting{Key: developer.EnabledSettingKey, Value: enabled}); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		store: database, developerEnabled: true, logger: regionTestLogger(), maxRequestBodyBytes: 4096,
		updateRepository: "owner/repo", publicIPs: make(map[string]cachedPublicIP),
		devices: &publicIPDeviceController{fakeDeviceController: fakeDeviceController{entry: device.Device{ID: "physical-1"}}},
	}
	runtime := server.cellularDataRuntime()
	runtime.start(ctx)
	requested := runtime.request("ec20", "physical-1", device.NetworkRequest{Enabled: true})
	waitContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if status, waitErr := runtime.wait(waitContext, "ec20", requested.Revision); waitErr != nil || !status.Connected {
		t.Fatalf("connected status = %+v, err = %v", status, waitErr)
	}
	return server
}

func consumeRequest(t *testing.T, server *Server, config store.Device, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/api/devices/ec20/network/consume", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.handleCellularDataConsume(recorder, request, config)
	return recorder
}

func TestDataConsumeDownloadsRequestedBytesThroughDeviceInterface(t *testing.T) {
	server := newDataConsumeTestServer(t)
	var gotInterface, gotURL string
	var gotLimit int64
	server.consumeDownload = func(_ context.Context, networkInterface, rawURL string, limit int64) (exportproxy.ConsumeResult, error) {
		gotInterface, gotURL, gotLimit = networkInterface, rawURL, limit
		return exportproxy.ConsumeResult{RequestedBytes: limit, PayloadBytes: limit, Complete: true}, nil
	}

	recorder := consumeRequest(t, server, connectedConsumeDevice, http.MethodPost, `{"kilobytes":10}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if gotInterface != "wwan-test" || gotLimit != 10240 {
		t.Fatalf("download interface = %q, limit = %d", gotInterface, gotLimit)
	}
	if !strings.HasPrefix(gotURL, "https://github.com/owner/repo/releases/latest/download/vocat-") {
		t.Fatalf("download URL = %q", gotURL)
	}
	var response struct {
		Data dataConsumeResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.PayloadBytes != 10240 || response.Data.RequestedBytes != 10240 || !response.Data.Complete {
		t.Fatalf("response = %+v", response.Data)
	}
	if _, stillBusy := server.dataConsumeBusy["ec20"]; stillBusy {
		t.Fatal("device stayed busy after the download finished")
	}
}

func TestDataConsumeRejectsRequestsThatMustNotDownload(t *testing.T) {
	server := newDataConsumeTestServer(t)
	called := false
	server.consumeDownload = func(context.Context, string, string, int64) (exportproxy.ConsumeResult, error) {
		called = true
		return exportproxy.ConsumeResult{}, nil
	}
	finish, admitted := server.beginDataConsume("busy")
	if !admitted {
		t.Fatal("first run was not admitted")
	}
	defer finish()
	busyDevice := connectedConsumeDevice
	busyDevice.ID = "busy"
	busyRuntime := server.cellularDataRuntime()
	requested := busyRuntime.request("busy", "physical-1", device.NetworkRequest{Enabled: true})
	waitContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := busyRuntime.wait(waitContext, "busy", requested.Revision); err != nil {
		t.Fatal(err)
	}

	disabled := connectedConsumeDevice
	disabled.NetworkEnabled = false
	vowifi := connectedConsumeDevice
	vowifi.VoWiFiEnabled = true
	disconnected := connectedConsumeDevice
	disconnected.ID = "no-session"
	noInterface := connectedConsumeDevice
	noInterface.Interface = " "
	tests := []struct {
		name   string
		config store.Device
		method string
		body   string
		status int
		code   string
	}{
		{"wrong method", connectedConsumeDevice, http.MethodGet, "", http.StatusMethodNotAllowed, "method_not_allowed"},
		{"zero", connectedConsumeDevice, http.MethodPost, `{"kilobytes":0}`, http.StatusBadRequest, "invalid_request"},
		{"too large", connectedConsumeDevice, http.MethodPost, `{"kilobytes":10241}`, http.StatusBadRequest, "invalid_request"},
		{"data disabled", disabled, http.MethodPost, `{"kilobytes":1}`, http.StatusConflict, "cellular_data_disabled"},
		{"vowifi", vowifi, http.MethodPost, `{"kilobytes":1}`, http.StatusConflict, "cellular_data_disabled"},
		{"not connected", disconnected, http.MethodPost, `{"kilobytes":1}`, http.StatusConflict, "cellular_data_not_connected"},
		{"no interface", noInterface, http.MethodPost, `{"kilobytes":1}`, http.StatusConflict, "cellular_interface_missing"},
		{"already running", busyDevice, http.MethodPost, `{"kilobytes":1}`, http.StatusConflict, "data_consume_in_progress"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := consumeRequest(t, server, test.config, test.method, test.body)
			if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), test.code) {
				t.Fatalf("status = %d, body = %s, want %d %s", recorder.Code, recorder.Body, test.status, test.code)
			}
		})
	}
	if called {
		t.Fatal("a rejected request started a download")
	}
}

func TestDataConsumeRequiresDeveloperMode(t *testing.T) {
	server := newDataConsumeTestServer(t)
	server.developerEnabled = false
	recorder := consumeRequest(t, server, connectedConsumeDevice, http.MethodPost, `{"kilobytes":1}`)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "developer_mode_required") {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
}

func TestDataConsumeReportsFailureAndPartialDownload(t *testing.T) {
	server := newDataConsumeTestServer(t)
	server.consumeDownload = func(_ context.Context, _, _ string, limit int64) (exportproxy.ConsumeResult, error) {
		return exportproxy.ConsumeResult{RequestedBytes: limit}, errors.New("no route")
	}
	recorder := consumeRequest(t, server, connectedConsumeDevice, http.MethodPost, `{"kilobytes":1}`)
	if recorder.Code != http.StatusBadGateway || !strings.Contains(recorder.Body.String(), "data_consume_failed") {
		t.Fatalf("failed status = %d, body = %s", recorder.Code, recorder.Body)
	}

	server.consumeDownload = func(_ context.Context, _, _ string, limit int64) (exportproxy.ConsumeResult, error) {
		return exportproxy.ConsumeResult{RequestedBytes: limit, PayloadBytes: 100}, errors.New("timeout")
	}
	recorder = consumeRequest(t, server, connectedConsumeDevice, http.MethodPost, `{"kilobytes":1}`)
	var response struct {
		Data dataConsumeResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || response.Data.PayloadBytes != 100 || response.Data.Complete {
		t.Fatalf("partial status = %d, response = %+v", recorder.Code, response.Data)
	}
}
