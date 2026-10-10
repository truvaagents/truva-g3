package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// This optional, read-only example protocol has no framework dependency or
// maintenance store knowledge. Components opt in through a registered internal
// capability. The operator supplies its credential per read; it is not saved.
const applicationInspectionCapability = "inspect_application_records_v1"

var applicationReadTimeout = 15 * time.Second
var applicationReadMaxBytes = 1 << 20

func applicationReadLimits(lookup func(string) (string, bool)) (time.Duration, int, error) {
	timeout, maximum := 15*time.Second, 1<<20
	if raw, present := lookup("TRUVAG3_VIEWER_APPLICATION_READ_TIMEOUT"); present {
		value, err := time.ParseDuration(raw)
		if err != nil || value < time.Second || value > time.Minute {
			return 0, 0, errors.New("TRUVAG3_VIEWER_APPLICATION_READ_TIMEOUT requires a duration in [1s, 1m]")
		}
		timeout = value
	}
	if raw, present := lookup("TRUVAG3_VIEWER_APPLICATION_READ_MAX_BYTES"); present {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 4096 || value > 8<<20 {
			return 0, 0, errors.New("TRUVAG3_VIEWER_APPLICATION_READ_MAX_BYTES requires bytes in [4096, 8388608]")
		}
		maximum = value
	}
	return timeout, maximum, nil
}

func configureApplicationReads() error {
	timeout, maximum, err := applicationReadLimits(os.LookupEnv)
	if err == nil {
		applicationReadTimeout, applicationReadMaxBytes = timeout, maximum
	}
	return err
}

func inspectionURL(service ServiceInfo) (string, error) {
	if service.Address == "" || strings.ContainsAny(service.Address, "/\\?#@ \t\r\n") || service.Port < 1 || service.Port > 65535 {
		return "", errors.New("invalid registered inspector address")
	}
	for _, capability := range service.Capabilities {
		if capability.Name != applicationInspectionCapability || !capability.Internal {
			continue
		}
		if !strings.HasPrefix(capability.Endpoint, "/") || strings.HasPrefix(capability.Endpoint, "//") || strings.ContainsAny(capability.Endpoint, "\\?#\r\n") {
			return "", errors.New("invalid registered inspector endpoint")
		}
		return "http://" + net.JoinHostPort(service.Address, strconv.Itoa(service.Port)) + capability.Endpoint, nil
	}
	return "", errors.New("service has no read-only application inspector version 1")
}

func readApplicationRecord(ctx context.Context, service ServiceInfo, authorization string, input json.RawMessage) ([]byte, int, error) {
	target, err := inspectionURL(service)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if !strings.HasPrefix(authorization, "Bearer ") || len(authorization) > 4096 || strings.ContainsAny(authorization, "\r\n") {
		return nil, http.StatusUnauthorized, errors.New("component read credential required")
	}
	if len(input) == 0 || len(input) > 16384 || !json.Valid(input) || bytes.TrimSpace(input)[0] != '{' {
		return nil, http.StatusBadRequest, errors.New("bounded JSON object required")
	}
	// Registry-owned address, not an operator-supplied URL. Redirects are refused
	// so a component cannot redirect its read credential to another origin.
	// #nosec G107,G704 -- validated discovery-owned component endpoint.
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(input))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", authorization)
	request.Header.Set("X-TruvaG3-Request-ID", core.GetRequestID(ctx))
	client := telemetry.NewTracedHTTPClient(nil)
	client.Timeout = applicationReadTimeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return nil, http.StatusBadGateway, errors.New("registered application inspector unavailable")
	}
	defer closeHTTPBody(response.Body)
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(applicationReadMaxBytes)+1))
	if err != nil || len(data) > applicationReadMaxBytes || !json.Valid(data) {
		return nil, http.StatusBadGateway, errors.New("application inspector returned invalid or oversized JSON")
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, http.StatusBadGateway, errors.New("application inspector redirect rejected")
	}
	return data, response.StatusCode, nil
}

func handleApplicationRecords(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	ctx := core.ExtractRequestContext(request.Context(), request)
	requestID := core.GetRequestID(ctx)
	if id := telemetry.GetBaggage(ctx)["request_id"]; id != "" {
		requestID = id
	}
	if requestID == "" {
		requestID = "viewer-read-" + rand.Text()
	}
	ctx = core.WithRequestID(ctx, requestID)
	ctx, correlationErr := telemetry.WithBaggageExact(ctx, "request_id", requestID)
	telemetry.SetSpanAttributes(ctx, attribute.String("request_id", requestID))
	ctx, end := telemetry.StartChildSpan(ctx, "viewer.application_records")
	defer end()
	request = request.WithContext(ctx)
	observed := &inspectionStatusWriter{ResponseWriter: writer}
	writer = observed
	component := "unknown"
	defer func() {
		status := observed.status
		if status == 0 {
			status = http.StatusOK
		}
		outcome := "success"
		if status >= 400 {
			outcome = "error"
		}
		telemetry.AddSpanEvent(ctx, "viewer.application_records.read", attribute.String("request_id", requestID), attribute.String("component", component), attribute.Int("http.status_code", status))
		telemetry.RecordToolCall("registry-viewer", applicationInspectionCapability, float64(time.Since(started).Milliseconds()), outcome)
		if viewerLogger != nil {
			viewerLogger.InfoWithContext(ctx, "Application inspection returned", map[string]any{"operation": "application_records", "request_id": requestID, "status": outcome, "http_status": status, "duration_ms": time.Since(started).Milliseconds(), "component": component})
		}
	}()
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("X-TruvaG3-Request-ID", requestID)
	fail := func(status int, code string) {
		telemetry.RecordSpanError(ctx, fmt.Errorf("application inspection %s", code))
		writer.WriteHeader(status)
		writeJSON(writer, map[string]string{"error": code})
	}
	if correlationErr != nil {
		fail(http.StatusBadRequest, "INVALID_READ_CORRELATION")
		return
	}
	if request.Method != http.MethodPost {
		fail(http.StatusMethodNotAllowed, "POST_REQUIRED")
		return
	}
	// Same-origin operator action. Unlike the public catalogue this endpoint has
	// no permissive CORS and no ETag cache of authorized application records.
	if origin := request.Header.Get("Origin"); origin != "" && origin != "http://"+request.Host && origin != "https://"+request.Host {
		fail(http.StatusForbidden, "ORIGIN_REJECTED")
		return
	}
	var input struct {
		ServiceID string          `json:"service_id"`
		Input     json.RawMessage `json:"input"`
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, 32769))
	if err != nil || len(data) > 32768 {
		fail(http.StatusBadRequest, "INVALID_INSPECTION_INPUT")
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.ServiceID == "" || len(input.ServiceID) > 256 {
		fail(http.StatusBadRequest, "INVALID_INSPECTION_INPUT")
		return
	}
	services, err := getRedisServices()
	if err != nil {
		fail(http.StatusServiceUnavailable, "DISCOVERY_UNAVAILABLE")
		return
	}
	var selected *ServiceInfo
	for i := range services {
		if services[i].ID == input.ServiceID {
			selected = &services[i]
			break
		}
	}
	if selected == nil {
		fail(http.StatusNotFound, "REGISTERED_COMPONENT_NOT_FOUND")
		return
	}
	component = selected.Name
	result, status, err := readApplicationRecord(ctx, *selected, request.Header.Get("Authorization"), input.Input)
	if err != nil {
		fail(status, "APPLICATION_INSPECTION_REJECTED")
		return
	}
	if status >= 400 {
		telemetry.RecordSpanError(ctx, fmt.Errorf("registered inspector returned HTTP %d", status))
	}
	writer.WriteHeader(status)
	if _, err := writer.Write(result); err != nil {
		telemetry.RecordSpanError(ctx, err)
	}
}

type inspectionStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *inspectionStatusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *inspectionStatusWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}
