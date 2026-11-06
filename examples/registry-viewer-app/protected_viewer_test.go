package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func protectedViewerTestEnvironment() map[string]string {
	return map[string]string{
		"TRUVAG3_VIEWER_PROTECTED_MODE":     "true",
		"TRUVAG3_VIEWER_REQUESTER_ISSUER":   "https://issuer.example/realms/team-b",
		"TRUVAG3_VIEWER_REQUESTER_AUDIENCE": "viewer-team-b",
		"TRUVAG3_VIEWER_RELAY_ORIGINS":      `["https://review-agent.team-b.svc.cluster.local:8443"]`,
		"TRUVAG3_VIEWER_JAEGER_PATH":        "/observability/tracing",
	}
}

func resolveProtectedViewerTestConfig(t *testing.T, env map[string]string) *protectedViewerConfig {
	t.Helper()
	cfg, err := resolveProtectedViewerConfig(func(key string) (string, bool) {
		value, present := env[key]
		return value, present
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestProtectedViewerConfiguration(t *testing.T) {
	for _, env := range []map[string]string{{}, {"TRUVAG3_VIEWER_PROTECTED_MODE": "false"}} {
		if cfg := resolveProtectedViewerTestConfig(t, env); cfg != nil {
			t.Fatal("ordinary mode unexpectedly enabled protection")
		}
	}
	for _, name := range []string{"TRUVAG3_VIEWER_REQUESTER_ISSUER", "TRUVAG3_VIEWER_REQUESTER_AUDIENCE", "TRUVAG3_VIEWER_RELAY_ORIGINS", "TRUVAG3_VIEWER_JAEGER_PATH", "TRUVAG3_SECURITY_SHARED", "TRUVAG3_SECURITY_SHARED_RELAYS"} {
		t.Run("disabled setting/"+name, func(t *testing.T) {
			_, err := resolveProtectedViewerConfig(func(key string) (string, bool) { return "", key == name })
			if err == nil {
				t.Fatal("present security setting was ignored while disabled")
			}
		})
	}
	for _, test := range []struct{ name, key, value string }{
		{"invalid mode", "TRUVAG3_VIEWER_PROTECTED_MODE", "1"},
		{"empty issuer", "TRUVAG3_VIEWER_REQUESTER_ISSUER", ""},
		{"insecure issuer", "TRUVAG3_VIEWER_REQUESTER_ISSUER", "http://issuer.example/realm"},
		{"issuer userinfo", "TRUVAG3_VIEWER_REQUESTER_ISSUER", "https://user:secret-canary@issuer.example"},
		{"issuer query", "TRUVAG3_VIEWER_REQUESTER_ISSUER", "https://issuer.example?secret-canary"},
		{"issuer fragment", "TRUVAG3_VIEWER_REQUESTER_ISSUER", "https://issuer.example#secret-canary"},
		{"empty audience", "TRUVAG3_VIEWER_REQUESTER_AUDIENCE", ""},
		{"no origins", "TRUVAG3_VIEWER_RELAY_ORIGINS", "[]"},
		{"null origins", "TRUVAG3_VIEWER_RELAY_ORIGINS", "null"},
		{"nonstring origin", "TRUVAG3_VIEWER_RELAY_ORIGINS", "[12]"},
		{"implicit port", "TRUVAG3_VIEWER_RELAY_ORIGINS", `["https://agent.example"]`},
		{"zero port", "TRUVAG3_VIEWER_RELAY_ORIGINS", `["https://agent.example:0"]`},
		{"large port", "TRUVAG3_VIEWER_RELAY_ORIGINS", `["https://agent.example:65536"]`},
		{"wildcard origin", "TRUVAG3_VIEWER_RELAY_ORIGINS", `["https://*.example:443"]`},
		{"origin userinfo", "TRUVAG3_VIEWER_RELAY_ORIGINS", `["https://user:secret-canary@agent.example:443"]`},
		{"origin query", "TRUVAG3_VIEWER_RELAY_ORIGINS", `["https://agent.example:443?secret-canary"]`},
		{"origin fragment", "TRUVAG3_VIEWER_RELAY_ORIGINS", `["https://agent.example:443#secret-canary"]`},
		{"origin path", "TRUVAG3_VIEWER_RELAY_ORIGINS", `["https://agent.example:443/inspector"]`},
		{"opaque origin", "TRUVAG3_VIEWER_RELAY_ORIGINS", `["https:agent.example:443"]`},
		{"duplicate canonical origin", "TRUVAG3_VIEWER_RELAY_ORIGINS", `["http://AGENT.example:080/","http://agent.example:80"]`},
		{"ambiguous scheme", "TRUVAG3_VIEWER_RELAY_ORIGINS", `["https://agent.example:443","http://agent.example:443"]`},
		{"absolute Jaeger URL", "TRUVAG3_VIEWER_JAEGER_PATH", "https://secret-canary.example/trace"},
		{"scheme relative Jaeger", "TRUVAG3_VIEWER_JAEGER_PATH", "//secret-canary.example/trace"},
		{"Jaeger dot segment", "TRUVAG3_VIEWER_JAEGER_PATH", "/observability/../tracing"},
		{"Jaeger encoded", "TRUVAG3_VIEWER_JAEGER_PATH", "/%74racing"},
		{"Jaeger backslash", "TRUVAG3_VIEWER_JAEGER_PATH", `/observability\tracing`},
		{"Jaeger browser tab authority", "TRUVAG3_VIEWER_JAEGER_PATH", "/\t/other.invalid"},
		{"Jaeger DEL", "TRUVAG3_VIEWER_JAEGER_PATH", "/tracing\x7f"},
		{"Jaeger noncanonical space", "TRUVAG3_VIEWER_JAEGER_PATH", "/trace ui"},
		{"Jaeger query", "TRUVAG3_VIEWER_JAEGER_PATH", "/tracing?secret-canary"},
		{"Jaeger fragment", "TRUVAG3_VIEWER_JAEGER_PATH", "/tracing#secret-canary"},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := protectedViewerTestEnvironment()
			env[test.key] = test.value
			cfg, err := resolveProtectedViewerConfig(func(key string) (string, bool) { value, ok := env[key]; return value, ok })
			if err == nil || cfg != nil || strings.Contains(err.Error(), "secret-canary") {
				t.Fatalf("invalid configuration returned config=%v error=%v", cfg != nil, err)
			}
		})
	}
	// Browser URL parsing removes ASCII TAB/CR/LF. This otherwise becomes a
	// network-path reference to another origin before /trace/{id} is appended.
	if normalized := strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace("/\t/other.invalid"); normalized != "//other.invalid" {
		t.Fatal("browser-normalization regression fixture no longer exercises an external authority")
	}
	for _, name := range []string{"TRUVAG3_VIEWER_REQUESTER_ISSUER", "TRUVAG3_VIEWER_REQUESTER_AUDIENCE", "TRUVAG3_VIEWER_RELAY_ORIGINS", "TRUVAG3_VIEWER_JAEGER_PATH"} {
		env := protectedViewerTestEnvironment()
		delete(env, name)
		if _, err := resolveProtectedViewerConfig(func(key string) (string, bool) { value, ok := env[key]; return value, ok }); err == nil {
			t.Fatalf("missing %s accepted", name)
		}
	}
	env := protectedViewerTestEnvironment()
	env["TRUVAG3_VIEWER_RELAY_ORIGINS"] = `["https://REVIEW-agent.team-b.svc.cluster.local:08443/","http://other-namespace.example:8081","https://[::1]:9443"]`
	env["TRUVAG3_VIEWER_JAEGER_PATH"] = "/"
	cfg := resolveProtectedViewerTestConfig(t, env)
	if origin, err := cfg.agentOrigin("review-agent.team-b.svc.cluster.local", 8443); err != nil || origin != "https://review-agent.team-b.svc.cluster.local:8443" || cfg.jaegerPath != "/" {
		t.Fatalf("HTTPS discovery binding = %q, %v", origin, err)
	}
	if origin, err := cfg.agentOrigin("other-namespace.example", 8081); err != nil || origin != "http://other-namespace.example:8081" {
		t.Fatalf("alternate discovery binding = %q, %v", origin, err)
	}
	if _, err := cfg.agentOrigin("other-namespace.example", 80); err == nil {
		t.Fatal("unregistered discovery port admitted")
	}
	if _, err := cfg.agentOrigin("https://other-namespace.example", 8081); err == nil {
		t.Fatal("a discovered hostname was rewritten from an arbitrary URL")
	}
	if origin, err := (*protectedViewerConfig)(nil).agentOrigin("ordinary-agent", 8080); err != nil || origin != "http://ordinary-agent:8080" {
		t.Fatalf("ordinary discovery changed: %q %v", origin, err)
	}
	env["TRUVAG3_VIEWER_RELAY_ORIGINS"] = "[\n \"https://agent.example:443\"\n]"
	resolveProtectedViewerTestConfig(t, env) // JSON formatting is not a URL control character.
}

// These are parser/transport fixtures with a dummy signature. They do not prove
// JWT signature verification, gateway/mesh enforcement, authorization, or CSRF.
func protectedViewerToken(t *testing.T, overrides map[string]any) string {
	t.Helper()
	claims := map[string]any{"iss": "https://issuer.example/realms/team-b", "aud": "viewer-team-b", "sub": "requester-a", "exp": time.Now().Add(time.Hour).Unix()}
	for key, value := range overrides {
		claims[key] = value
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return protectedViewerRawToken(string(payload))
}

func protectedViewerRawToken(payload string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".c3ludGhldGlj"
}

func TestProtectedViewerRequesterGate(t *testing.T) {
	cfg := resolveProtectedViewerTestConfig(t, protectedViewerTestEnvironment())
	valid := protectedViewerToken(t, nil)
	exp := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	for _, test := range []struct {
		name    string
		headers []string
		pass    bool
	}{
		{"string audience", []string{"Bearer " + valid}, true},
		{"array audience", []string{"bearer " + protectedViewerToken(t, map[string]any{"aud": []string{"other", "viewer-team-b"}})}, true},
		{"missing", nil, false}, {"duplicate", []string{"Bearer " + valid, "Bearer " + valid}, false},
		{"oversized", []string{"Bearer " + strings.Repeat("x", viewerRequesterHeaderLimit)}, false},
		{"wrong scheme", []string{"Basic " + valid}, false},
		{"multiple tokens", []string{"Bearer " + valid + " Bearer " + valid}, false},
		{"control", []string{"Bearer\v" + valid}, false},
		{"missing segment", []string{"Bearer e30.e30"}, false},
		{"empty segment", []string{"Bearer e30..c2ln"}, false},
		{"invalid payload", []string{"Bearer e30.not-json.c2ln"}, false},
		{"wrong issuer", []string{"Bearer " + protectedViewerToken(t, map[string]any{"iss": "https://other.example"})}, false},
		{"empty subject", []string{"Bearer " + protectedViewerToken(t, map[string]any{"sub": " "})}, false},
		{"expired", []string{"Bearer " + protectedViewerToken(t, map[string]any{"exp": time.Now().Unix()})}, false},
		{"fractional expiry", []string{"Bearer " + protectedViewerToken(t, map[string]any{"exp": 1.5})}, false},
		{"string expiry", []string{"Bearer " + protectedViewerToken(t, map[string]any{"exp": exp})}, false},
		{"wrong audience", []string{"Bearer " + protectedViewerToken(t, map[string]any{"aud": "other"})}, false},
		{"malformed array", []string{"Bearer " + protectedViewerToken(t, map[string]any{"aud": []any{"viewer-team-b", 4}})}, false},
		{"null array entry", []string{"Bearer " + protectedViewerToken(t, map[string]any{"aud": []any{"viewer-team-b", nil}})}, false},
		{"object audience", []string{"Bearer " + protectedViewerToken(t, map[string]any{"aud": map[string]any{"viewer-team-b": true}})}, false},
		{"duplicate aud", []string{"Bearer " + protectedViewerRawToken(`{"iss":"https://issuer.example/realms/team-b","sub":"a","aud":"other","aud":"viewer-team-b","exp":`+exp+`}`)}, false},
		{"duplicate exp", []string{"Bearer " + protectedViewerRawToken(`{"iss":"https://issuer.example/realms/team-b","sub":"a","aud":"viewer-team-b","exp":1,"exp":`+exp+`}`)}, false},
		{"trailing claims", []string{"Bearer " + protectedViewerRawToken(`{} {}`)}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := cfg.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) }))
			request := httptest.NewRequest(http.MethodGet, "/api/services", nil)
			request.Header["Authorization"] = test.headers
			writer := httptest.NewRecorder()
			handler.ServeHTTP(writer, request)
			if called != test.pass || writer.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("called=%v status=%d cache=%q", called, writer.Code, writer.Header().Get("Cache-Control"))
			}
			if !test.pass && (writer.Code != http.StatusUnauthorized || strings.Contains(writer.Body.String(), valid)) {
				t.Fatal("rejection status or credential exposure")
			}
		})
	}
	for _, test := range []struct {
		method, target string
		pass           bool
	}{
		{http.MethodGet, "/api/health", true}, {http.MethodGet, "/api/readiness", true},
		{http.MethodGet, "/static/js/api.js", true}, {http.MethodPost, "/api/health", false},
		{http.MethodHead, "/api/readiness", false}, {http.MethodGet, "/api/health/", false},
		{http.MethodGet, "/api/health?query=1", false},
	} {
		writer := httptest.NewRecorder()
		cfg.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(writer, httptest.NewRequest(test.method, test.target, nil))
		if (writer.Code == http.StatusNoContent) != test.pass {
			t.Fatalf("probe exception %s %s returned %d", test.method, test.target, writer.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/services", nil)
	request.Header["Authorization"] = []string{"Bearer " + valid}
	request.Header["authorization"] = []string{"Bearer " + valid}
	writer := httptest.NewRecorder()
	cfg.middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("case-variant duplicate was admitted") })).ServeHTTP(writer, request)
	if writer.Code != http.StatusUnauthorized {
		t.Fatal("case-variant duplicate Authorization was not rejected")
	}
}

func protectedViewerContext(t *testing.T, cfg *protectedViewerConfig, token string) context.Context {
	t.Helper()
	var ctx context.Context
	request := httptest.NewRequest(http.MethodGet, "/api/services", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	cfg.middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), request)
	if ctx == nil {
		t.Fatal("fixture requester was rejected")
	}
	return ctx
}

func TestProtectedViewerHeaderIsolationAndInspection(t *testing.T) {
	cfg := resolveProtectedViewerTestConfig(t, protectedViewerTestEnvironment())
	token := protectedViewerToken(t, nil)
	request := httptest.NewRequest(http.MethodPost, "/api/services", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	for _, name := range []string{"Cookie", "Proxy-Authorization", "X-User-ID", "X-Forwarded-User", "X-Forwarded-Email", "X-Forwarded-Access-Token", "X-Auth-Request-Access-Token"} {
		request.Header.Set(name, "untrusted-secret-canary")
	}
	request.Header["x-user-id"] = []string{"untrusted-lowercase"}
	request.Header.Set("Traceparent", "00-trace-parent-01")
	request.Header.Set("Baggage", "request_id=original")
	before := request.Header.Clone()
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	var captured *http.Request
	cfg.middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { captured = r })).ServeHTTP(httptest.NewRecorder(), request)
	if captured == nil || captured == request || !reflect.DeepEqual(request.Header, before) {
		t.Fatal("middleware did not isolate original headers")
	}
	for name, values := range captured.Header {
		if strings.EqualFold(name, "Authorization") || strings.Contains(strings.Join(values, ""), "untrusted") {
			t.Fatal("normal handler received an identity credential")
		}
	}
	if captured.Header.Get("Traceparent") != request.Header.Get("Traceparent") || captured.Header.Get("Baggage") != request.Header.Get("Baggage") || captured.Context().Done() != ctx.Done() {
		t.Fatal("middleware replaced tracing or cancellation context")
	}
	cancel()
	if !errors.Is(captured.Context().Err(), context.Canceled) {
		t.Fatal("request cancellation was not retained")
	}
	for _, protection := range []*protectedViewerConfig{nil, cfg} {
		called := false
		handler := protection.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusAccepted) }))
		inspection := httptest.NewRequest(http.MethodPost, "/api/application-records", nil)
		inspection.Header.Set("Authorization", "Bearer "+token)
		writer := httptest.NewRecorder()
		handler.ServeHTTP(writer, inspection)
		if protection == nil {
			if !called || writer.Code != http.StatusAccepted {
				t.Fatal("ordinary inspection was intercepted")
			}
		} else if called || writer.Code != http.StatusServiceUnavailable || !strings.Contains(writer.Body.String(), "APPLICATION_INSPECTION_UNAVAILABLE_IN_PROTECTED_MODE") || writer.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("protected inspection did not stop before handler lookup/network access")
		}
	}
}

type protectedViewerRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip protectedViewerRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestProtectedViewerRelayAdmission(t *testing.T) {
	cfg := resolveProtectedViewerTestConfig(t, protectedViewerTestEnvironment())
	ctx := protectedViewerContext(t, cfg, protectedViewerToken(t, nil))
	const origin = "https://review-agent.team-b.svc.cluster.local:8443"
	for _, test := range []struct {
		method, suffix string
		pass           bool
	}{
		{http.MethodGet, "/hitl/checkpoints", true}, {http.MethodGet, "/hitl/checkpoints?request_id=req-a&limit=1&offset=0", true},
		{http.MethodGet, "/hitl/checkpoints?limit=100&offset=1000000", true}, {http.MethodGet, "/hitl/checkpoints/checkpoint_A-1", true},
		{http.MethodPost, "/hitl/command", true}, {http.MethodPost, "/hitl/resume/checkpoint_A-1", true}, {http.MethodPost, "/hitl/resume-sync/checkpoint_A-1", true},
		{http.MethodHead, "/hitl/checkpoints", false}, {http.MethodPost, "/hitl/checkpoints", false}, {http.MethodGet, "/hitl/command", false},
		{http.MethodPost, "/orchestrate/natural", false}, {http.MethodGet, "/application/inspect", false},
		{http.MethodGet, "/hitl/checkpoints/_bad", false}, {http.MethodGet, "/hitl/checkpoints/" + strings.Repeat("a", 257), false},
		{http.MethodGet, "/hitl/checkpoints/a/b", false}, {http.MethodGet, "/hitl/checkpoints/%61", false},
		{http.MethodGet, "/hitl/checkpoints/../command", false}, {http.MethodGet, "/hitl//checkpoints", false},
		{http.MethodGet, "/hitl/checkpoints/a?request_id=req-a", false}, {http.MethodPost, "/hitl/command?limit=1", false},
		{http.MethodPost, "/hitl/resume/a?", false}, {http.MethodGet, "/hitl/checkpoints?limit=0", false},
		{http.MethodGet, "/hitl/checkpoints?limit=101", false}, {http.MethodGet, "/hitl/checkpoints?offset=1000001", false},
		{http.MethodGet, "/hitl/checkpoints?offset=-1", false}, {http.MethodGet, "/hitl/checkpoints?limit=1&limit=2", false},
		{http.MethodGet, "/hitl/checkpoints?limit=1&%6cimit=2", false}, {http.MethodGet, "/hitl/checkpoints?request_id=", false},
		{http.MethodGet, "/hitl/checkpoints?request_id=" + strings.Repeat("r", 257), false}, {http.MethodGet, "/hitl/checkpoints?unknown=1", false},
		{http.MethodGet, "/hitl/checkpoints?limit=%zz", false}, {http.MethodGet, "/hitl/checkpoints?&", false},
		{http.MethodPost, "/untrusted-stored-address/hitl/command", false},
	} {
		t.Run(test.method+test.suffix, func(t *testing.T) {
			called := false
			client := cfg.client(&http.Client{Transport: protectedViewerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				called = true
				return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header), Request: r}, nil
			})})
			request, err := http.NewRequestWithContext(ctx, test.method, origin+test.suffix, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Transport.RoundTrip(request)
			if called != test.pass || (err == nil) != test.pass {
				t.Fatalf("called=%v err=%v expected admitted=%v", called, err, test.pass)
			}
		})
	}
	for _, target := range []string{
		"http://review-agent.team-b.svc.cluster.local:8443/hitl/checkpoints", "https://other.example:8443/hitl/checkpoints",
		"https://review-agent.team-b.svc.cluster.local:443/hitl/checkpoints", "https://review-agent.team-b.svc.cluster.local/hitl/checkpoints",
		"https://REVIEW-agent.team-b.svc.cluster.local:8443/hitl/checkpoints", "https://review-agent.team-b.svc.cluster.local:08443/hitl/checkpoints",
		"https://user:secret-canary@review-agent.team-b.svc.cluster.local:8443/hitl/checkpoints", origin + "/hitl/checkpoints#fragment",
	} {
		transport := cfg.client(&http.Client{Transport: protectedViewerRoundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid recipient reached transport")
			return nil, nil
		})}).Transport
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transport.RoundTrip(request); err == nil || strings.Contains(err.Error(), "secret-canary") {
			t.Fatalf("invalid origin admitted or exposed: %v", err)
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/hitl/checkpoints", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "other.example:8443"
	transport := cfg.client(&http.Client{Transport: protectedViewerRoundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("conflicting Host forwarded"); return nil, nil })}).Transport
	if _, err := transport.RoundTrip(request); err == nil {
		t.Fatal("conflicting Request.Host admitted")
	}
}

func TestProtectedViewerRelayRequesterIsolationAndExpiry(t *testing.T) {
	cfg := resolveProtectedViewerTestConfig(t, protectedViewerTestEnvironment())
	const target = "https://review-agent.team-b.svc.cluster.local:8443/hitl/checkpoints"
	client := cfg.client(&http.Client{Transport: protectedViewerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		credential := request.Context().Value(viewerRequesterKey{}).(viewerRequester)
		if request.Header.Get("Authorization") != "Bearer "+credential.token || request.Header.Get("Cookie") != "" || request.Header.Get("X-User-ID") != "" || request.Header.Get("Traceparent") != "original-trace" {
			return nil, errors.New("requester isolation or tracing failed")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
	})})
	var wait sync.WaitGroup
	failures := make(chan error, 24)
	for index := range 24 {
		token := protectedViewerToken(t, map[string]any{"sub": fmt.Sprintf("requester-%d", index)})
		ctx := protectedViewerContext(t, cfg, token)
		wait.Go(func() {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			if err != nil {
				failures <- err
				return
			}
			request.Header.Set("Authorization", "Bearer untrusted-other-user")
			request.Header.Set("Cookie", "untrusted-cookie")
			request.Header.Set("X-User-ID", "untrusted-user")
			request.Header.Set("Traceparent", "original-trace")
			before := request.Header.Clone()
			_, err = client.Transport.RoundTrip(request)
			if err != nil {
				failures <- err
			} else if !reflect.DeepEqual(before, request.Header) {
				failures <- errors.New("outbound original header mutated")
			}
		})
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	blocked := cfg.client(&http.Client{Transport: protectedViewerRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("missing, expired or cancelled credential reached downstream")
		return nil, nil
	})}).Transport.(*protectedViewerTransport)
	ctx := protectedViewerContext(t, cfg, protectedViewerToken(t, nil))
	blocked.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	for _, contextValue := range []context.Context{context.Background(), ctx} {
		request, err := http.NewRequestWithContext(contextValue, http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := blocked.RoundTrip(request); err == nil {
			t.Fatal("missing/expired credential was admitted")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	request, err := http.NewRequestWithContext(cancelled, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocked.RoundTrip(request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation identity = %v", err)
	}
	sentinel := errors.New("transport secret-canary")
	failedClient := cfg.client(&http.Client{Transport: protectedViewerRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, sentinel })})
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failedClient.Transport.RoundTrip(request); !errors.Is(err, sentinel) || strings.Contains(err.Error(), "secret-canary") {
		t.Fatal("transport diagnostic was exposed or control-flow cause was lost")
	}
}

func TestProtectedViewerClientRejectsRedirectsAndClonesTransport(t *testing.T) {
	var recipientCalls atomic.Int32
	recipient := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		recipientCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(recipient.Close)
	var bearer atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer.Store(r.Header.Get("Authorization"))
		http.Redirect(w, r, recipient.URL+"/hitl/checkpoints", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	env := protectedViewerTestEnvironment()
	origins, err := json.Marshal([]string{server.URL})
	if err != nil {
		t.Fatal(err)
	}
	env["TRUVAG3_VIEWER_RELAY_ORIGINS"] = string(origins)
	cfg := resolveProtectedViewerTestConfig(t, env)
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	base.Proxy = http.ProxyFromEnvironment
	original := &http.Client{Transport: base, Timeout: 3 * time.Second}
	client := cfg.client(original)
	transport := client.Transport.(*protectedViewerTransport).next.(*http.Transport)
	t.Cleanup(transport.CloseIdleConnections)
	if client == original || transport == base || transport.Proxy != nil || base.Proxy == nil || client.Timeout != original.Timeout || transport.TLSClientConfig == base.TLSClientConfig || transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("dedicated client altered or failed to isolate original transport")
	}
	if (*protectedViewerConfig)(nil).client(original) != original {
		t.Fatal("ordinary client changed")
	}
	token := protectedViewerToken(t, nil)
	ctx := protectedViewerContext(t, cfg, token)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/hitl/command", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusTemporaryRedirect || recipientCalls.Load() != 0 || bearer.Load() != "Bearer "+token {
		t.Fatal("redirect followed or requester not forwarded to original admitted recipient")
	}
	if err := client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatal("redirect refusal changed")
	}
}
