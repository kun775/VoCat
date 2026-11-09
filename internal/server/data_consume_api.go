package server

import (
	"context"
	"net/http"
	"runtime"
	"strings"
	"time"

	"vocat/internal/exportproxy"
	"vocat/internal/store"
	"vocat/internal/update"
)

const (
	minDataConsumeKilobytes int64 = 1
	maxDataConsumeKilobytes int64 = 10240
	// Stay inside the HTTP server's 40-second write timeout so a slow roaming
	// link can still report how much it consumed.
	dataConsumeTimeout = 30 * time.Second
	// Late packets and the connection close keep arriving after the body ends.
	dataConsumeCounterSettle = 500 * time.Millisecond
)

type dataConsumeResponse struct {
	RequestedBytes    int64 `json:"requested_bytes"`
	PayloadBytes      int64 `json:"payload_bytes"`
	InterfaceMeasured bool  `json:"interface_measured"`
	InterfaceRXBytes  int64 `json:"interface_rx_bytes"`
	InterfaceTXBytes  int64 `json:"interface_tx_bytes"`
	Complete          bool  `json:"complete"`
	DurationMS        int64 `json:"duration_ms"`
}

// beginDataConsume admits one download per device, so repeated clicks cannot
// silently multiply the data charged to the SIM.
func (s *Server) beginDataConsume(deviceID string) (func(), bool) {
	s.dataConsumeMu.Lock()
	defer s.dataConsumeMu.Unlock()
	if _, busy := s.dataConsumeBusy[deviceID]; busy {
		return nil, false
	}
	if s.dataConsumeBusy == nil {
		s.dataConsumeBusy = make(map[string]struct{})
	}
	s.dataConsumeBusy[deviceID] = struct{}{}
	return func() {
		s.dataConsumeMu.Lock()
		delete(s.dataConsumeBusy, deviceID)
		s.dataConsumeMu.Unlock()
	}, true
}

func (s *Server) runDataConsume(ctx context.Context, networkInterface, rawURL string, limit int64) (exportproxy.ConsumeResult, error) {
	consume := s.consumeDownload
	if consume == nil {
		consume = exportproxy.ConsumeDownload
	}
	return consume(ctx, networkInterface, rawURL, limit)
}

// handleCellularDataConsume downloads a fixed number of bytes over the active
// cellular session to keep an eSIM subscription in use. Interface counters are
// reported next to the payload because TLS and redirects are billed as well.
func (s *Server) handleCellularDataConsume(w http.ResponseWriter, r *http.Request, config store.Device) bool {
	if !requireMethod(w, r, http.MethodPost) {
		return true
	}
	if !s.developerActive(r.Context()) {
		writeError(w, http.StatusForbidden, "developer_mode_required", "roaming data is available only in developer mode")
		return true
	}
	var request struct {
		Kilobytes int64 `json:"kilobytes"`
	}
	if err := s.decodeJSON(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return true
	}
	if request.Kilobytes < minDataConsumeKilobytes || request.Kilobytes > maxDataConsumeKilobytes {
		writeError(w, http.StatusBadRequest, "invalid_request", "kilobytes must be between 1 and 10240")
		return true
	}
	if !config.NetworkEnabled || config.VoWiFiEnabled {
		writeError(w, http.StatusConflict, "cellular_data_disabled", "enable roaming data before consuming data")
		return true
	}
	if !s.cellularDataRuntime().status(config.ID, config.NetworkEnabled).Connected {
		writeError(w, http.StatusConflict, "cellular_data_not_connected", "the cellular data session is not connected")
		return true
	}
	networkInterface := strings.TrimSpace(config.Interface)
	if networkInterface == "" {
		writeError(w, http.StatusConflict, "cellular_interface_missing", "the device has no cellular network interface")
		return true
	}
	rawURL, err := update.LatestAssetURL(s.updateRepository, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		s.logger.Error("build data consumption URL failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		return true
	}
	finish, admitted := s.beginDataConsume(config.ID)
	if !admitted {
		writeError(w, http.StatusConflict, "data_consume_in_progress", "a data consumption run is already in progress")
		return true
	}
	defer finish()

	ctx, cancel := context.WithTimeout(r.Context(), dataConsumeTimeout)
	defer cancel()
	rxBefore, txBefore, counterErr := netIfCounters(networkInterface)
	started := time.Now()
	result, consumeErr := s.runDataConsume(ctx, networkInterface, rawURL, request.Kilobytes*1024)
	duration := time.Since(started)
	if consumeErr != nil && result.PayloadBytes == 0 {
		s.logger.Warn("consume cellular data failed", "device_id", config.ID, "interface", networkInterface, "error", consumeErr)
		s.recordAudit(r.Context(), "admin", "device.data_consume", "device", config.ID, "failure", r.RemoteAddr)
		writeError(w, http.StatusBadGateway, "data_consume_failed", consumeErr.Error())
		return true
	}
	response := dataConsumeResponse{
		RequestedBytes: result.RequestedBytes,
		PayloadBytes:   result.PayloadBytes,
		Complete:       result.Complete,
		DurationMS:     duration.Milliseconds(),
	}
	if counterErr == nil {
		time.Sleep(dataConsumeCounterSettle)
		rxAfter, txAfter, afterErr := netIfCounters(networkInterface)
		// A reconnect resets the counters and makes the difference meaningless.
		if afterErr == nil && rxAfter >= rxBefore && txAfter >= txBefore {
			response.InterfaceMeasured = true
			response.InterfaceRXBytes = int64(rxAfter - rxBefore)
			response.InterfaceTXBytes = int64(txAfter - txBefore)
		}
	}
	if consumeErr != nil {
		s.logger.Warn("cellular data consumption ended early", "device_id", config.ID, "interface", networkInterface, "payload_bytes", result.PayloadBytes, "error", consumeErr)
	}
	s.recordAudit(r.Context(), "admin", "device.data_consume", "device", config.ID, "success", r.RemoteAddr)
	writeJSON(w, http.StatusOK, map[string]any{"data": response})
	return true
}
