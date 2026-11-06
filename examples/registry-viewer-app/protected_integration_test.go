package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func integrationProtection(t *testing.T) *protectedViewerConfig {
	t.Helper()
	settings := map[string]string{
		"TRUVAG3_VIEWER_PROTECTED_MODE":     "true",
		"TRUVAG3_VIEWER_REQUESTER_ISSUER":   "https://identity.example/realm",
		"TRUVAG3_VIEWER_REQUESTER_AUDIENCE": "viewer",
		"TRUVAG3_VIEWER_RELAY_ORIGINS":      `["https://review.team.svc.cluster.local:8443"]`,
		"TRUVAG3_VIEWER_JAEGER_PATH":        "/observability/tracing",
	}
	cfg, err := resolveProtectedViewerConfig(func(key string) (string, bool) { value, ok := settings[key]; return value, ok })
	if err != nil {
		t.Fatal(err)
	}
	old := viewerProtection
	viewerProtection = cfg
	t.Cleanup(func() { viewerProtection = old })
	return cfg
}

// Unsigned parser/transport fixture. This does not authenticate a requester.
func integrationAuthorization() string {
	claims := fmt.Sprintf(`{"iss":"https://identity.example/realm","aud":"viewer","sub":"fixture","exp":%d}`, time.Now().Add(time.Hour).Unix())
	return "Bearer e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".fixture"
}

func TestProtectedServicesAdvertiseInspectionLimitAndBypassETag(t *testing.T) {
	cfg := integrationProtection(t)
	oldMock := useMock
	useMock = true
	t.Cleanup(func() { useMock = oldMock })
	handler := cfg.middleware(apiMiddleware(handleServices))
	request := httptest.NewRequest(http.MethodGet, "/api/services", nil)
	request.Header.Set("Authorization", integrationAuthorization())
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, request)
	if first.Code != http.StatusOK || first.Header().Get("ETag") != "" || !responseNoStore(first.Header()) {
		t.Fatalf("protected response: status=%d headers=%v", first.Code, first.Header())
	}
	var result RegistryResponse
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ViewerCapabilities.ApplicationRecords || len(result.Services) == 0 {
		t.Fatal("missing services or inaccurate inspection capability")
	}
	request.Header.Set("If-None-Match", `"anything"`)
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request)
	if second.Code != http.StatusOK {
		t.Fatalf("protected response incorrectly cached: %d", second.Code)
	}

	viewerProtection = nil
	ordinary := httptest.NewRecorder()
	handleServices(ordinary, httptest.NewRequest(http.MethodGet, "/api/services", nil))
	if err := json.Unmarshal(ordinary.Body.Bytes(), &result); err != nil || !result.ViewerCapabilities.ApplicationRecords {
		t.Fatalf("ordinary inspection unavailable: %v", err)
	}
}

func TestProtectedWholeMuxGatesInspectionAndTraceNavigation(t *testing.T) {
	cfg := integrationProtection(t)
	called := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api/application-records", func(http.ResponseWriter, *http.Request) { called = true })
	mux.HandleFunc("/api/traces/", handleJaegerTraceRedirect)
	handler := cfg.middleware(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/application-records", strings.NewReader(`{"service_id":"ignored"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || called || !responseNoStore(response.Header()) {
		t.Fatal("inspection bypassed requester gate")
	}
	request.Header.Set("Authorization", integrationAuthorization())
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || called || !strings.Contains(response.Body.String(), "APPLICATION_INSPECTION_UNAVAILABLE_IN_PROTECTED_MODE") {
		t.Fatal("protected inspection attempted an inspector lookup")
	}
	trace := "1234567890abcdef1234567890abcdef"
	request = httptest.NewRequest(http.MethodGet, "/api/traces/"+trace, nil)
	request.Header.Set("Authorization", integrationAuthorization())
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTemporaryRedirect || response.Header().Get("Location") != "/observability/tracing/trace/"+trace || !responseNoStore(response.Header()) {
		t.Fatalf("trace redirect status=%d location=%s", response.Code, response.Header().Get("Location"))
	}
	cfg.jaegerPath = "/"
	target, err := jaegerBrowserTraceURL(trace)
	if err != nil || target != "/trace/"+trace {
		t.Fatalf("root join=%q err=%v", target, err)
	}
}
