package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSkillProxyETagPreservesSemanticToken(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, compressed := range []bool{false, true} {
			t.Run(fmt.Sprintf("explicit=%t/gzip=%t", explicit, compressed), func(t *testing.T) {
				handler := withCORS(withGzip(withSkillProxyETag(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("ETag", `"revision-17-exact"`)
					if explicit {
						w.WriteHeader(http.StatusCreated)
						w.WriteHeader(http.StatusTeapot)
					}
					_, _ = io.WriteString(w, `{"actual":"body"}`)
				})).ServeHTTP))
				request := httptest.NewRequest(http.MethodGet, "/api/v1/skills/test/demo", nil)
				if compressed {
					request.Header.Set("Accept-Encoding", "gzip")
				}
				response := httptest.NewRecorder()
				handler(response, request)
				wantStatus := http.StatusOK
				if explicit {
					wantStatus = http.StatusCreated
				}
				if response.Code != wantStatus {
					t.Fatalf("status=%d", response.Code)
				}
				for _, name := range []string{"ETag", "X-TruvaG3-Skill-ETag"} {
					if response.Header().Get(name) != `"revision-17-exact"` {
						t.Fatalf("%s=%q", name, response.Header().Get(name))
					}
				}
				var body io.Reader = response.Body
				if compressed {
					reader, err := gzip.NewReader(body)
					if err != nil {
						t.Fatal(err)
					}
					defer reader.Close()
					body = reader
				}
				data, err := io.ReadAll(body)
				if err != nil || string(data) != `{"actual":"body"}` {
					t.Fatalf("body=%s error=%v", data, err)
				}
				if !strings.Contains(response.Header().Get("Access-Control-Expose-Headers"), "X-TruvaG3-Skill-ETag") {
					t.Fatal("mirror not exposed")
				}
			})
		}
	}
	response := httptest.NewRecorder()
	withSkillProxyETag(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Header().Get("X-TruvaG3-Skill-ETag") != "" {
		t.Fatal("invented semantic token")
	}
}

func TestBodyETagOnlyCachesEligibleResponses(t *testing.T) {
	successful := httptest.NewRecorder()
	withETag(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "same body") })(successful, httptest.NewRequest(http.MethodGet, "/", nil))
	matchingETag := successful.Header().Get("ETag")
	if matchingETag == "" {
		t.Fatal("successful representation has no ETag")
	}
	for _, scenario := range []struct {
		name, method, cacheControl string
		status                     int
		cache                      bool
	}{
		{"get", http.MethodGet, "", 200, true},
		{"head", http.MethodHead, "", 200, true},
		{"post", http.MethodPost, "", 200, false},
		{"denied", http.MethodGet, "", 403, false},
		{"error", http.MethodGet, "", 500, false},
		{"partial", http.MethodGet, "", 206, false},
		{"empty", http.MethodGet, "", 204, false},
		{"no-store", http.MethodGet, "private, NO-STORE", 200, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			handler := withETag(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", scenario.cacheControl)
				w.WriteHeader(scenario.status)
				_, _ = io.WriteString(w, "same body")
			})
			first := httptest.NewRecorder()
			handler(first, httptest.NewRequest(scenario.method, "/", nil))
			etag := first.Header().Get("ETag")
			if (etag != "") != scenario.cache {
				t.Fatalf("ETag=%q", etag)
			}
			request := httptest.NewRequest(scenario.method, "/", nil)
			// The known body hash would have converted even an error to 304 before.
			if etag == "" {
				etag = matchingETag
			}
			request.Header.Set("If-None-Match", etag)
			second := httptest.NewRecorder()
			handler(second, request)
			want := scenario.status
			if scenario.cache {
				want = http.StatusNotModified
			}
			if second.Code != want {
				t.Fatalf("status=%d want=%d", second.Code, want)
			}
			if !scenario.cache && scenario.status != http.StatusNoContent && second.Body.String() != "same body" {
				t.Fatalf("lost body: %q", second.Body.String())
			}
		})
	}
	recorder := httptest.NewRecorder()
	withETag(func(http.ResponseWriter, *http.Request) {})(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("empty handler status=%d", recorder.Code)
	}
}
