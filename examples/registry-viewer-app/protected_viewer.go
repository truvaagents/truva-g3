package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// viewerProtection is installed once at startup, before handlers or clients run.
// Decoding a requester here only rejects bad claims; the non-bypassable external
// gateway/mesh must authenticate signatures, authorize operations and enforce CSRF.
var viewerProtection *protectedViewerConfig

type protectedViewerConfig struct {
	issuer, audience, jaegerPath string
	relayOrigins                 map[string]struct{}
	originsByAuthority           map[string]string
}

const viewerRequesterHeaderLimit = 16 * 1024 // Fixed credential admission bound.

var (
	viewerHostnameLabel   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	viewerTokenSegment    = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	viewerCheckpointRoute = regexp.MustCompile(`^/hitl/(checkpoints/[A-Za-z0-9][A-Za-z0-9_-]{0,255}|(?:resume|resume-sync)/[A-Za-z0-9][A-Za-z0-9_-]{0,255})$`)
)

func resolveProtectedViewerConfig(lookup func(string) (string, bool)) (*protectedViewerConfig, error) {
	for _, name := range []string{"TRUVAG3_SECURITY_SHARED", "TRUVAG3_SECURITY_SHARED_RELAYS"} {
		if _, present := lookup(name); present {
			return nil, errors.New("legacy Viewer security configuration is unsupported; migrate to TRUVAG3_VIEWER_PROTECTED_MODE and its requester/relay settings")
		}
	}
	mode, present := lookup("TRUVAG3_VIEWER_PROTECTED_MODE")
	if present && mode != "true" && mode != "false" {
		return nil, errors.New("TRUVAG3_VIEWER_PROTECTED_MODE must be true or false")
	}
	names := []string{"TRUVAG3_VIEWER_REQUESTER_ISSUER", "TRUVAG3_VIEWER_REQUESTER_AUDIENCE", "TRUVAG3_VIEWER_RELAY_ORIGINS", "TRUVAG3_VIEWER_JAEGER_PATH"}
	values := make([]string, len(names))
	for index, name := range names {
		value, supplied := lookup(name)
		if mode != "true" {
			if supplied {
				return nil, errors.New("protected Viewer settings require TRUVAG3_VIEWER_PROTECTED_MODE=true")
			}
			continue
		}
		if !supplied || value == "" || (index != 2 && (strings.TrimSpace(value) != value || strings.IndexFunc(value, func(character rune) bool { return character < 32 || character == 127 }) >= 0)) {
			return nil, fmt.Errorf("%s must be supplied with a valid value", name)
		}
		values[index] = value
	}
	if mode != "true" {
		return nil, nil
	}
	issuer, err := url.Parse(values[0])
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" || strings.HasSuffix(issuer.Host, ":") || issuer.User != nil || issuer.Opaque != "" || issuer.RawQuery != "" || issuer.ForceQuery || strings.Contains(values[0], "#") || strings.Contains(values[0], "\\") {
		return nil, errors.New("invalid protected Viewer requester issuer")
	}
	if _, err := canonicalViewerHost(issuer.Hostname()); err != nil {
		return nil, errors.New("invalid protected Viewer requester issuer")
	}
	if issuer.Port() != "" {
		if _, err := canonicalViewerPort(issuer.Port()); err != nil {
			return nil, errors.New("invalid protected Viewer requester issuer")
		}
	}
	basePath := values[3]
	baseURL := &url.URL{Path: basePath}
	if !strings.HasPrefix(basePath, "/") || strings.HasPrefix(basePath, "//") || strings.ContainsAny(basePath, "\\%?#") || path.Clean(basePath) != basePath || baseURL.EscapedPath() != basePath {
		return nil, errors.New("invalid protected Viewer Jaeger base path")
	}
	var origins []string
	if json.Unmarshal([]byte(values[2]), &origins) != nil || len(origins) == 0 {
		return nil, errors.New("protected Viewer relay origins must be a nonempty JSON string array")
	}
	cfg := &protectedViewerConfig{
		issuer: values[0], audience: values[1], jaegerPath: basePath,
		relayOrigins: make(map[string]struct{}, len(origins)), originsByAuthority: make(map[string]string, len(origins)),
	}
	for _, raw := range origins {
		origin, authority, err := canonicalViewerOrigin(raw)
		if err != nil {
			return nil, errors.New("invalid protected Viewer relay origin")
		}
		if _, duplicate := cfg.originsByAuthority[authority]; duplicate {
			return nil, errors.New("duplicate or ambiguous protected Viewer relay origin")
		}
		cfg.relayOrigins[origin] = struct{}{}
		cfg.originsByAuthority[authority] = origin
	}
	return cfg, nil
}

func canonicalViewerHost(host string) (string, error) {
	if addr, err := netip.ParseAddr(host); err == nil && addr.Zone() == "" {
		return addr.String(), nil
	}
	host = strings.ToLower(host)
	if len(host) == 0 || len(host) > 253 {
		return "", errors.New("invalid hostname")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) > 63 || !viewerHostnameLabel.MatchString(label) {
			return "", errors.New("invalid hostname")
		}
	}
	return host, nil
}

func canonicalViewerPort(raw string) (string, error) {
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("invalid port")
	}
	return strconv.Itoa(port), nil
}

func canonicalViewerOrigin(raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\\#") || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", errors.New("invalid relay origin")
	}
	host, hostErr := canonicalViewerHost(u.Hostname())
	port, portErr := canonicalViewerPort(u.Port())
	if hostErr != nil || portErr != nil {
		return "", "", errors.New("invalid relay origin")
	}
	authority := net.JoinHostPort(host, port)
	return u.Scheme + "://" + authority, authority, nil
}

func (cfg *protectedViewerConfig) agentOrigin(host string, port int) (string, error) {
	if cfg == nil {
		return fmt.Sprintf("http://%s:%d", host, port), nil
	}
	host, err := canonicalViewerHost(host)
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("invalid discovered HITL authority")
	}
	origin, admitted := cfg.originsByAuthority[net.JoinHostPort(host, strconv.Itoa(port))]
	if !admitted {
		return "", errors.New("discovered HITL authority is not an admitted relay")
	}
	return origin, nil
}

type viewerRequesterKey struct{}
type viewerRequester struct {
	token   string
	expires int64
}

func (cfg *protectedViewerConfig) requesterContext(r *http.Request) (context.Context, error) {
	var headers []string
	for name, values := range r.Header {
		if strings.EqualFold(name, "Authorization") {
			headers = append(headers, values...)
		}
	}
	if len(headers) != 1 || len(headers[0]) > viewerRequesterHeaderLimit || strings.ContainsAny(headers[0], "\r\n\x00") {
		return nil, errors.New("invalid requester credential")
	}
	for _, character := range headers[0] {
		if character < 32 || character == 127 {
			return nil, errors.New("invalid requester credential")
		}
	}
	auth := strings.Fields(headers[0])
	if len(auth) != 2 || !strings.EqualFold(auth[0], "Bearer") {
		return nil, errors.New("invalid requester credential")
	}
	parts := strings.Split(auth[1], ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid requester credential")
	}
	for _, part := range parts {
		if !viewerTokenSegment.MatchString(part) {
			return nil, errors.New("invalid requester credential")
		}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !utf8.Valid(payload) {
		return nil, errors.New("invalid requester credential")
	}
	claims, err := decodeViewerRequesterClaims(payload)
	if err != nil {
		return nil, errors.New("invalid requester claims")
	}
	var issuer, subject string
	var expires int64
	if json.Unmarshal(claims["iss"], &issuer) != nil || json.Unmarshal(claims["sub"], &subject) != nil || json.Unmarshal(claims["exp"], &expires) != nil || issuer != cfg.issuer || strings.TrimSpace(subject) == "" || expires <= time.Now().Unix() || !cfg.acceptsAudience(claims["aud"]) {
		return nil, errors.New("invalid or expired requester claims")
	}
	return context.WithValue(r.Context(), viewerRequesterKey{}, viewerRequester{token: auth[1], expires: expires}), nil
}

func decodeViewerRequesterClaims(payload []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("invalid requester claims")
	}
	claims := make(map[string]json.RawMessage)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid requester claims")
		}
		name, ok := key.(string)
		if _, duplicate := claims[name]; !ok || duplicate {
			return nil, errors.New("invalid requester claims")
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, errors.New("invalid requester claims")
		}
		claims[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errors.New("invalid requester claims")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("invalid requester claims")
	}
	return claims, nil
}

func (cfg *protectedViewerConfig) acceptsAudience(raw json.RawMessage) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch audience := value.(type) {
	case string:
		return audience == cfg.audience
	case []any:
		matched := false
		for _, item := range audience {
			text, ok := item.(string)
			if !ok || text == "" {
				return false
			}
			matched = matched || text == cfg.audience
		}
		return matched
	default:
		return false
	}
}

func stripViewerIdentityHeaders(header http.Header) {
	for key := range header {
		for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization", "X-User-ID", "X-Forwarded-User", "X-Forwarded-Email", "X-Forwarded-Access-Token", "X-Auth-Request-Access-Token"} {
			if strings.EqualFold(key, name) {
				delete(header, key)
				break
			}
		}
	}
}

func (cfg *protectedViewerConfig) middleware(next http.Handler) http.Handler {
	if cfg == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		if r.Method == http.MethodGet && (r.URL.Path == "/api/health" || r.URL.Path == "/api/readiness") && r.URL.RawPath == "" && r.URL.RawQuery == "" && !r.URL.ForceQuery {
			next.ServeHTTP(w, r)
			return
		}
		ctx, err := cfg.requesterContext(r)
		if err != nil {
			http.Error(w, "Requester credential required or invalid", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/api/application-records" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"code":"APPLICATION_INSPECTION_UNAVAILABLE_IN_PROTECTED_MODE","error":"APPLICATION_INSPECTION_UNAVAILABLE_IN_PROTECTED_MODE","message":"Application-record inspection is unavailable in protected mode."}`)
			return
		}
		cloned := r.Clone(ctx)
		stripViewerIdentityHeaders(cloned.Header)
		next.ServeHTTP(w, cloned)
	})
}

type protectedViewerTransport struct {
	cfg  *protectedViewerConfig
	next http.RoundTripper
	now  func() time.Time
}

type protectedViewerRelayError struct{ cause error }

func (err *protectedViewerRelayError) Error() string { return "protected HITL relay failed" }
func (err *protectedViewerRelayError) Unwrap() error { return err.cause }

func (cfg *protectedViewerConfig) client(original *http.Client) *http.Client {
	if cfg == nil {
		return original
	}
	if original == nil {
		original = http.DefaultClient
	}
	cloned := *original
	transport := original.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if base, ok := transport.(*http.Transport); ok {
		dedicated := base.Clone()
		dedicated.Proxy = nil
		if dedicated.TLSClientConfig != nil {
			dedicated.TLSClientConfig.InsecureSkipVerify = false
		}
		transport = dedicated
	}
	cloned.Transport = &protectedViewerTransport{cfg: cfg, next: transport, now: time.Now}
	cloned.Jar = nil
	cloned.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &cloned
}

func (transport *protectedViewerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	credential, ok := r.Context().Value(viewerRequesterKey{}).(viewerRequester)
	if !ok || credential.token == "" || credential.expires <= transport.now().Unix() {
		return nil, errors.New("requester credential missing or expired")
	}
	u := r.URL
	if u == nil || u.User != nil || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" || u.RawPath != "" || u.ForceQuery || r.RequestURI != "" || (r.Host != "" && r.Host != u.Host) {
		return nil, errors.New("invalid protected HITL recipient")
	}
	if _, admitted := transport.cfg.relayOrigins[u.Scheme+"://"+u.Host]; !admitted {
		return nil, errors.New("protected HITL recipient is not admitted")
	}
	collection := r.Method == http.MethodGet && u.Path == "/hitl/checkpoints"
	detail := r.Method == http.MethodGet && strings.HasPrefix(u.Path, "/hitl/checkpoints/") && viewerCheckpointRoute.MatchString(u.Path)
	write := r.Method == http.MethodPost && (u.Path == "/hitl/command" || (!strings.HasPrefix(u.Path, "/hitl/checkpoints/") && viewerCheckpointRoute.MatchString(u.Path)))
	if !collection && !detail && !write {
		return nil, errors.New("invalid protected HITL route")
	}
	if u.RawQuery != "" {
		if !collection || validateViewerRelayQuery(u.RawQuery) != nil {
			return nil, errors.New("invalid protected HITL query")
		}
	}
	cloned := r.Clone(r.Context())
	stripViewerIdentityHeaders(cloned.Header)
	cloned.Header.Set("Authorization", "Bearer "+credential.token)
	response, err := transport.next.RoundTrip(cloned)
	if err != nil {
		// Preserve cancellation/control-flow identity without exposing a transport's
		// potentially credential-bearing diagnostic to the Viewer's error handlers.
		return response, &protectedViewerRelayError{cause: err}
	}
	return response, nil
}

func validateViewerRelayQuery(raw string) error {
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) == 0 {
		return errors.New("invalid collection query")
	}
	for key, entries := range values {
		if len(entries) != 1 {
			return errors.New("duplicate collection query parameter")
		}
		switch key {
		case "request_id":
			if entries[0] == "" || len(entries[0]) > 256 {
				return errors.New("invalid collection request ID")
			}
		case "limit", "offset":
			number, err := strconv.Atoi(entries[0])
			if err != nil || number < 0 || (key == "limit" && (number < 1 || number > 100)) || (key == "offset" && number > 1000000) {
				return errors.New("invalid collection pagination")
			}
		default:
			return errors.New("unknown collection query parameter")
		}
	}
	return nil
}
