package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadOnlyApplicationInspector(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer labelled-read-token" || r.Method != http.MethodPost {
			t.Error("authorization or method changed")
		}
		switch r.URL.Path {
		case "/read":
			_, _ = w.Write([]byte(`{"success":true,"data":{"schema_version":1,"records":[],"automatic_reconsideration_enabled":false}}`))
		case "/redirect":
			w.Header().Set("Location", "/read")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "/large":
			_, _ = fmt.Fprint(w, `{"data":"`+strings.Repeat("x", 1<<20)+`"}`)
		case "/bad":
			_, _ = fmt.Fprint(w, "not JSON")
		}
	}))
	defer server.Close()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(port)
	service := ServiceInfo{Address: host, Port: n, Capabilities: []Capability{{Name: applicationInspectionCapability, Internal: true, Endpoint: "/read"}}}
	for _, tc := range []struct {
		name, endpoint, token string
		expected              int
	}{
		{"valid", "/read", "Bearer labelled-read-token", 200}, {"no_credential", "/read", "", 401}, {"redirect", "/redirect", "Bearer labelled-read-token", 502},
		{"large", "/large", "Bearer labelled-read-token", 502}, {"bad_json", "/bad", "Bearer labelled-read-token", 502}, {"arbitrary_endpoint", "//example.invalid", "Bearer labelled-read-token", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := service
			local.Capabilities = []Capability{{Name: applicationInspectionCapability, Internal: true, Endpoint: tc.endpoint}}
			before := requests.Load()
			data, status, err := readApplicationRecord(context.Background(), local, tc.token, json.RawMessage(`{"label":"fixture"}`))
			if status != tc.expected || (err == nil) != (status == 200) {
				t.Fatalf("status=%d error=%v", status, err)
			}
			if status == 200 && !json.Valid(data) {
				t.Fatal("payload changed")
			}
			if (status == 401 || status == 400) && requests.Load() != before {
				t.Fatal("rejected input reached component")
			}
			if tc.name == "redirect" && requests.Load()-before != 1 {
				t.Fatal("read credential followed redirect")
			}
		})
	}
	service.Capabilities[0].Internal = false
	if _, err := inspectionURL(service); err == nil {
		t.Fatal("non-opt-in capability accepted")
	}
}

func TestApplicationReadLimits(t *testing.T) {
	for _, tc := range []struct {
		name, timeout, bytes string
		timeoutSet, bytesSet bool
		invalid              bool
	}{
		{name: "defaults"},
		{name: "configured", timeout: "30s", bytes: "65536", timeoutSet: true, bytesSet: true},
		{name: "empty_timeout", timeoutSet: true, invalid: true},
		{name: "zero_timeout", timeout: "0s", timeoutSet: true, invalid: true},
		{name: "large_timeout", timeout: "61s", timeoutSet: true, invalid: true},
		{name: "empty_bytes", bytesSet: true, invalid: true},
		{name: "zero_bytes", bytes: "0", bytesSet: true, invalid: true},
		{name: "large_bytes", bytes: "8388609", bytesSet: true, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			duration, maximum, err := applicationReadLimits(func(key string) (string, bool) {
				if key == "TRUVAG3_VIEWER_APPLICATION_READ_TIMEOUT" {
					return tc.timeout, tc.timeoutSet
				}
				return tc.bytes, tc.bytesSet
			})
			if (err != nil) != tc.invalid {
				t.Fatalf("error=%v", err)
			}
			if tc.name == "defaults" && (duration != 15*time.Second || maximum != 1<<20) {
				t.Fatal("sensible absent defaults changed")
			}
			if tc.name == "configured" && (duration != 30*time.Second || maximum != 65536) {
				t.Fatal("explicit limits ignored")
			}
		})
	}
}
