package main

import "net/http"

// skillProxyETagWriter preserves the Skills API's semantic concurrency token
// through proxies that omit the standard ETag header.
type skillProxyETagWriter struct {
	http.ResponseWriter
	written bool
}

func (w *skillProxyETagWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *skillProxyETagWriter) WriteHeader(status int) {
	if w.written {
		return
	}
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.written = true
	if etag := w.Header().Get("ETag"); etag != "" {
		w.Header().Set("X-TruvaG3-Skill-ETag", etag)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *skillProxyETagWriter) Write(body []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func withSkillProxyETag(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&skillProxyETagWriter{ResponseWriter: w}, r)
	})
}
