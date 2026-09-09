package main

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var jaegerTraceIDPattern = regexp.MustCompile(`^[[:xdigit:]]{16,32}$`)

var jaegerUIURL = strings.TrimRight(strings.TrimSpace(getEnvOrDefault("JAEGER_UI_URL", "http://jaeger.localhost")), "/")

// jaegerBrowserTraceURL is deliberately navigation-only. Registry Viewer does
// not query Jaeger for execution data; the stored trace ID is merely an
// optional pointer to a separately operated observability UI.
func jaegerBrowserTraceURL(traceID string) (string, error) {
	traceID = strings.TrimSpace(traceID)
	if !jaegerTraceIDPattern.MatchString(traceID) {
		return "", fmt.Errorf("trace ID is invalid")
	}
	base, err := url.Parse(jaegerUIURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") ||
		base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", fmt.Errorf("jaeger UI URL is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/trace/" + traceID
	return base.String(), nil
}

func handleJaegerTraceRedirect(w http.ResponseWriter, r *http.Request) {
	traceID := strings.TrimPrefix(r.URL.Path, "/api/traces/")
	target, err := jaegerBrowserTraceURL(traceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, target, http.StatusTemporaryRedirect)
}
