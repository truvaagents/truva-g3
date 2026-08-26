package main

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const maxRequestBodyBytes = 2 << 20

type config struct {
	listenAddr        string
	ollamaBaseURL     string
	ollamaModel       string
	clientID          string
	clientSecret      string
	appKey            string
	deployment        string
	apiVersion        string
	responseModel     string
	requiredStop      string
	tokenTTL          time.Duration
	requestTimeout    time.Duration
	strictAuthHeaders bool
}

func loadConfig() (config, error) {
	cfg := config{
		listenAddr:        envOr("SIMULATOR_ADDR", "127.0.0.1:18080"),
		ollamaBaseURL:     strings.TrimRight(envOr("OLLAMA_BASE_URL", "http://127.0.0.1:11434"), "/"),
		ollamaModel:       envOr("OLLAMA_MODEL", "llama3.2"),
		clientID:          envOr("ENTERPRISE_CLIENT_ID", "travel-chat-agent"),
		clientSecret:      envOr("ENTERPRISE_CLIENT_SECRET", "local-enterprise-secret"),
		appKey:            envOr("ENTERPRISE_APP_KEY", "travel-chat-app"),
		deployment:        envOr("ENTERPRISE_DEPLOYMENT", "gpt-4o-mini"),
		apiVersion:        os.Getenv("ENTERPRISE_API_VERSION"),
		responseModel:     os.Getenv("ENTERPRISE_RESPONSE_MODEL"),
		requiredStop:      envOr("ENTERPRISE_REQUIRED_STOP", "<|im_end|>"),
		strictAuthHeaders: envBool("ENTERPRISE_STRICT_AUTH_HEADERS", true),
	}

	var err error
	if cfg.tokenTTL, err = envDuration("ENTERPRISE_TOKEN_TTL", 5*time.Minute); err != nil {
		return config{}, err
	}
	if cfg.requestTimeout, err = envDuration("OLLAMA_REQUEST_TIMEOUT", 3*time.Minute); err != nil {
		return config{}, err
	}
	if cfg.tokenTTL <= 0 {
		return config{}, errors.New("ENTERPRISE_TOKEN_TTL must be greater than zero")
	}
	if cfg.requestTimeout <= 0 {
		return config{}, errors.New("OLLAMA_REQUEST_TIMEOUT must be greater than zero")
	}
	return cfg, nil
}

type gateway struct {
	cfg        config
	httpClient *http.Client
	now        func() time.Time

	mu     sync.Mutex
	tokens map[string]time.Time
}

func newGateway(cfg config, client *http.Client) *gateway {
	if client == nil {
		client = &http.Client{Timeout: cfg.requestTimeout}
	}
	return &gateway{
		cfg:        cfg,
		httpClient: client,
		now:        time.Now,
		tokens:     make(map[string]time.Time),
	}
}

func (g *gateway) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", g.handleHealth)
	mux.HandleFunc("POST /oauth2/token", g.handleToken)
	mux.HandleFunc("POST /openai/deployments/{deployment}/chat/completions", g.handleChat)
	return mux
}

func (g *gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, g.cfg.ollamaBaseURL+"/api/tags", nil)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "build health request", "gateway_error")
		return
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable",
			"ollama": err.Error(),
		})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	status := http.StatusOK
	state := "healthy"
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status = http.StatusServiceUnavailable
		state = "unavailable"
	}
	writeJSON(w, status, map[string]any{
		"status":       state,
		"ollama":       g.cfg.ollamaBaseURL,
		"ollama_model": g.cfg.ollamaModel,
	})
}

func (g *gateway) handleToken(w http.ResponseWriter, r *http.Request) {
	clientID, clientSecret, ok := r.BasicAuth()
	if !ok || !secureEqual(clientID, g.cfg.clientID) || !secureEqual(clientSecret, g.cfg.clientSecret) {
		w.Header().Set("WWW-Authenticate", `Basic realm="enterprise-token"`)
		writeAPIError(w, http.StatusUnauthorized, "invalid client credentials", "invalid_client")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid form body", "invalid_request")
		return
	}
	if r.PostForm.Get("grant_type") != "client_credentials" {
		writeAPIError(w, http.StatusBadRequest, "grant_type must be client_credentials", "unsupported_grant_type")
		return
	}

	token, err := randomToken(32)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "could not issue access token", "gateway_error")
		return
	}
	g.mu.Lock()
	g.tokens[token] = g.now().Add(g.cfg.tokenTTL)
	g.mu.Unlock()

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   int(g.cfg.tokenTTL.Seconds()),
	})
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type enterpriseChatRequest struct {
	Messages    []chatMessage `json:"messages"`
	Model       string        `json:"model,omitempty"`
	User        string        `json:"user"`
	Stop        []string      `json:"stop"`
	Stream      bool          `json:"stream"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature *float64      `json:"temperature,omitempty"`
}

type ollamaChatRequest struct {
	Model    string         `json:"model"`
	Messages []chatMessage  `json:"messages"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"`
}

type ollamaChatResponse struct {
	Model      string      `json:"model"`
	Message    chatMessage `json:"message"`
	Done       bool        `json:"done"`
	DoneReason string      `json:"done_reason"`

	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}

func (g *gateway) handleChat(w http.ResponseWriter, r *http.Request) {
	if g.cfg.strictAuthHeaders && r.Header.Get("Authorization") != "" {
		writeAPIError(w, http.StatusBadRequest, "Authorization header is not accepted; use api-key", "invalid_auth_header")
		return
	}
	if !g.validToken(r.Header.Get("api-key")) {
		w.Header().Set("WWW-Authenticate", "ApiKey")
		writeAPIError(w, http.StatusUnauthorized, "missing, invalid, or expired api-key token", "invalid_token")
		return
	}

	deployment := r.PathValue("deployment")
	if deployment == "" || (g.cfg.deployment != "" && deployment != g.cfg.deployment) {
		writeAPIError(w, http.StatusNotFound, "unknown deployment", "deployment_not_found")
		return
	}
	if g.cfg.apiVersion != "" && r.URL.Query().Get("api-version") != g.cfg.apiVersion {
		writeAPIError(w, http.StatusBadRequest, "missing or incorrect api-version", "invalid_api_version")
		return
	}

	var input enterpriseChatRequest
	if err := decodeJSON(r.Body, &input); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	if len(input.Messages) == 0 {
		writeAPIError(w, http.StatusBadRequest, "messages must not be empty", "invalid_request")
		return
	}
	if input.Stream {
		writeAPIError(w, http.StatusBadRequest, "this simulator implements the captured non-streaming exchange only", "streaming_not_supported")
		return
	}
	if err := g.validateUser(input.User); err != nil {
		writeAPIError(w, http.StatusForbidden, err.Error(), "invalid_app_key")
		return
	}
	if g.cfg.requiredStop != "" && !contains(input.Stop, g.cfg.requiredStop) {
		writeAPIError(w, http.StatusBadRequest, fmt.Sprintf("stop must contain %q", g.cfg.requiredStop), "invalid_stop")
		return
	}

	options := make(map[string]any)
	if input.MaxTokens > 0 {
		options["num_predict"] = input.MaxTokens
	}
	if input.Temperature != nil {
		options["temperature"] = *input.Temperature
	}
	if len(input.Stop) > 0 {
		options["stop"] = input.Stop
	}

	ollamaRequest := ollamaChatRequest{
		Model:    g.cfg.ollamaModel,
		Messages: input.Messages,
		Stream:   false,
		Options:  options,
	}
	body, err := json.Marshal(ollamaRequest)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "could not encode Ollama request", "gateway_error")
		return
	}

	started := g.now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, g.cfg.ollamaBaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "could not build Ollama request", "gateway_error")
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "Ollama request failed: "+err.Error(), "upstream_error")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	upstreamBody, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBodyBytes))
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "could not read Ollama response", "upstream_error")
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(upstreamBody))
		if message == "" {
			message = resp.Status
		}
		writeAPIError(w, http.StatusBadGateway, "Ollama returned "+message, "upstream_error")
		return
	}

	var output ollamaChatResponse
	if err := json.Unmarshal(upstreamBody, &output); err != nil {
		writeAPIError(w, http.StatusBadGateway, "Ollama returned invalid JSON", "upstream_error")
		return
	}

	finishReason := output.DoneReason
	if finishReason == "" {
		finishReason = "stop"
	}
	model := g.cfg.responseModel
	if model == "" {
		model = deployment
	}
	durationMS := max(g.now().Sub(started).Milliseconds(), int64(0))
	totalTokens := output.PromptEvalCount + output.EvalCount

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-sim-" + mustRandomID(),
		"object":  "chat.completion",
		"created": g.now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"finish_reason": finishReason,
			"logprobs":      nil,
			"message": map[string]any{
				"role":        "assistant",
				"content":     output.Message.Content,
				"annotations": []any{},
				"refusal":     nil,
			},
			"content_filter_results": safeContentFilterResults(),
		}},
		"prompt_filter_results": []any{map[string]any{
			"prompt_index":           0,
			"content_filter_results": safeContentFilterResults(),
		}},
		"service_tier":       "default",
		"system_fingerprint": "ollama-simulator:" + g.cfg.ollamaModel,
		"usage": map[string]any{
			"prompt_tokens":     output.PromptEvalCount,
			"completion_tokens": output.EvalCount,
			"total_tokens":      totalTokens,
			"prompt_tokens_details": map[string]any{
				"audio_tokens":  0,
				"cached_tokens": 0,
			},
			"completion_tokens_details": map[string]any{
				"accepted_prediction_tokens": 0,
				"audio_tokens":               0,
				"reasoning_tokens":           0,
				"rejected_prediction_tokens": 0,
			},
			"latency_checkpoint": map[string]any{
				"engine_tbt_ms":        0,
				"engine_ttft_ms":       durationMS,
				"engine_ttlt_ms":       durationMS,
				"pre_inference_ms":     0,
				"service_tbt_ms":       0,
				"service_ttft_ms":      durationMS,
				"service_ttlt_ms":      durationMS,
				"total_duration_ms":    durationMS,
				"user_visible_ttft_ms": durationMS,
			},
		},
		"user": input.User,
	})
}

func (g *gateway) validateUser(raw string) error {
	if raw == "" {
		return errors.New("user must be a JSON string containing appkey")
	}
	var metadata struct {
		AppKey string `json:"appkey"`
	}
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return errors.New("user must contain valid JSON encoded as a string")
	}
	if !secureEqual(metadata.AppKey, g.cfg.appKey) {
		return errors.New("user appkey is missing or invalid")
	}
	return nil
}

func (g *gateway) validToken(token string) bool {
	if token == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	expiresAt, ok := g.tokens[token]
	if !ok {
		return false
	}
	if !g.now().Before(expiresAt) {
		delete(g.tokens, token)
		return false
	}
	return true
}

func safeContentFilterResults() map[string]any {
	result := make(map[string]any, 4)
	for _, category := range []string{"hate", "self_harm", "sexual", "violence"} {
		result[category] = map[string]any{"filtered": false, "severity": "safe"}
	}
	return result
}

func decodeJSON(body io.Reader, dst any) error {
	decoder := json.NewDecoder(io.LimitReader(body, maxRequestBodyBytes))
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("invalid JSON body: multiple JSON values")
	}
	return nil
}

func writeAPIError(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "enterprise_gateway_error",
			"code":    code,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write JSON response: %v", err)
	}
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func mustRandomID() string {
	id, err := randomToken(9)
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return id
}

func secureEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envBool(name string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration: %w", name, err)
	}
	return value, nil
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	g := newGateway(cfg, nil)
	server := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           g.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      cfg.requestTimeout + 10*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("enterprise simulator listening on http://%s", cfg.listenAddr)
	log.Printf("token endpoint: POST /oauth2/token")
	log.Printf("chat endpoint: POST /openai/deployments/%s/chat/completions", cfg.deployment)
	log.Printf("Ollama backend: %s (model %s)", cfg.ollamaBaseURL, cfg.ollamaModel)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
