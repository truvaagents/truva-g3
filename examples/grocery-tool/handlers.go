package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// ============================================================================
// HTTP Client for API Proxy
// ============================================================================

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

// APIErrorResponse represents an error from the grocery-store-api
type APIErrorResponse struct {
	Error         string `json:"error"`
	Code          string `json:"code"`
	RequestsMade  int64  `json:"requests_made,omitempty"`
	RequestsLimit int    `json:"requests_limit,omitempty"`
	RetryAfter    string `json:"retry_after,omitempty"`
	ErrorRate     string `json:"error_rate,omitempty"`
}

// ============================================================================
// Proxy Helper Functions
// ============================================================================

// makeAPIRequest makes a request to the grocery-store-api
func (g *GroceryTool) makeAPIRequest(ctx interface{}, method, path string, body interface{}) (*http.Response, error) {
	url := g.apiBaseURL + path

	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	return httpClient.Do(req)
}

// handleAPIError converts API error responses to ToolError
func (g *GroceryTool) handleAPIError(resp *http.Response) *core.ToolError {
	var apiErr APIErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiErr); err != nil {
		return &core.ToolError{
			Code:      "API_ERROR",
			Message:   fmt.Sprintf("API returned status %d", resp.StatusCode),
			Category:  core.CategoryServiceError,
			Retryable: resp.StatusCode >= 500,
		}
	}

	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return &core.ToolError{
			Code:      apiErr.Code,
			Message:   apiErr.Error,
			Category:  core.CategoryRateLimit,
			Retryable: true,
			Details: map[string]string{
				"retry_after":    apiErr.RetryAfter,
				"requests_made":  fmt.Sprintf("%d", apiErr.RequestsMade),
				"requests_limit": fmt.Sprintf("%d", apiErr.RequestsLimit),
			},
		}
	case http.StatusInternalServerError:
		return &core.ToolError{
			Code:      apiErr.Code,
			Message:   apiErr.Error,
			Category:  core.CategoryServiceError,
			Retryable: true,
			Details: map[string]string{
				"error_rate": apiErr.ErrorRate,
				"hint":       "This may be a simulated error for resilience testing",
			},
		}
	case http.StatusNotFound:
		return &core.ToolError{
			Code:      apiErr.Code,
			Message:   apiErr.Error,
			Category:  core.CategoryNotFound,
			Retryable: false,
		}
	case http.StatusBadRequest:
		return &core.ToolError{
			Code:      apiErr.Code,
			Message:   apiErr.Error,
			Category:  core.CategoryInputError,
			Retryable: false,
		}
	default:
		return &core.ToolError{
			Code:      "API_ERROR",
			Message:   apiErr.Error,
			Category:  core.CategoryServiceError,
			Retryable: resp.StatusCode >= 500,
		}
	}
}

// writeToolError writes a structured error response
func (g *GroceryTool) writeToolError(w http.ResponseWriter, status int, toolErr *core.ToolError) {
	w.Header().Set("Content-Type", "application/json")
	if toolErr.Category == core.CategoryRateLimit {
		if retryAfter, ok := toolErr.Details["retry_after"]; ok {
			// Extract just the number from "5s" format
			retryAfter = strings.TrimSuffix(retryAfter, "s")
			w.Header().Set("Retry-After", retryAfter)
		}
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error:   toolErr,
	})
}

// writeToolSuccess writes a successful response
func (g *GroceryTool) writeToolSuccess(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    data,
	})
}

// ============================================================================
// Capability Handlers - Proxy to grocery-store-api
// ============================================================================

// handleListProducts proxies list_products to the API
func (g *GroceryTool) handleListProducts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "list_products"),
	)

	g.Logger.InfoWithContext(ctx, "Processing list_products request (proxy to API)", map[string]interface{}{
		"api_url": g.apiBaseURL,
	})

	var req ListProductsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		g.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
			"error": err.Error(),
		})
		g.writeToolError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "INVALID_REQUEST",
			Message:   "Invalid request format",
			Category:  core.CategoryInputError,
			Retryable: false,
		})
		return
	}

	// Build query params
	path := "/products"
	if req.Category != "" {
		path += "?category=" + req.Category
	}

	// Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_grocery_api",
		attribute.String("path", path),
		attribute.String("category", req.Category),
	)

	// Make API request
	resp, err := g.makeAPIRequest(ctx, "GET", path, nil)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		g.Logger.ErrorWithContext(ctx, "API request failed", map[string]interface{}{
			"error": err.Error(),
		})
		g.writeToolError(w, http.StatusServiceUnavailable, &core.ToolError{
			Code:      "API_UNAVAILABLE",
			Message:   "Failed to connect to grocery store API: " + err.Error(),
			Category:  core.CategoryServiceError,
			Retryable: true,
		})
		return
	}
	defer resp.Body.Close()

	// Handle error responses
	if resp.StatusCode != http.StatusOK {
		toolErr := g.handleAPIError(resp)
		telemetry.RecordSpanError(ctx, fmt.Errorf("%s: %s", toolErr.Code, toolErr.Message))
		g.Logger.WarnWithContext(ctx, "API returned error", map[string]interface{}{
			"status": resp.StatusCode,
			"code":   toolErr.Code,
		})
		g.writeToolError(w, resp.StatusCode, toolErr)
		return
	}

	// Parse successful response
	var products []Product
	if err := json.NewDecoder(resp.Body).Decode(&products); err != nil {
		g.Logger.ErrorWithContext(ctx, "Failed to decode API response", map[string]interface{}{
			"error": err.Error(),
		})
		g.writeToolError(w, http.StatusInternalServerError, &core.ToolError{
			Code:      "PARSE_ERROR",
			Message:   "Failed to parse API response",
			Category:  core.CategoryServiceError,
			Retryable: true,
		})
		return
	}

	// Apply limit if specified
	if req.Limit > 0 && req.Limit < len(products) {
		products = products[:req.Limit]
	}

	response := ListProductsResponse{
		Products: products,
		Count:    len(products),
		Category: req.Category,
	}

	// Add success span event
	telemetry.AddSpanEvent(ctx, "products_listed",
		attribute.Int("count", len(products)),
		attribute.String("category", req.Category),
	)

	g.Logger.InfoWithContext(ctx, "list_products completed", map[string]interface{}{
		"count":    len(products),
		"category": req.Category,
	})

	g.writeToolSuccess(w, response)
}

// handleGetProduct proxies get_product to the API
func (g *GroceryTool) handleGetProduct(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_product"),
	)

	g.Logger.InfoWithContext(ctx, "Processing get_product request (proxy to API)", nil)

	var req GetProductRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		g.writeToolError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "INVALID_REQUEST",
			Message:   "Invalid request format: " + err.Error(),
			Category:  core.CategoryInputError,
			Retryable: false,
		})
		return
	}

	// Add span event before API call
	path := fmt.Sprintf("/products/%d", req.ProductID)
	telemetry.AddSpanEvent(ctx, "calling_grocery_api",
		attribute.String("path", path),
		attribute.Int("product_id", req.ProductID),
	)

	// Make API request
	resp, err := g.makeAPIRequest(ctx, "GET", path, nil)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		g.Logger.ErrorWithContext(ctx, "API request failed", map[string]interface{}{
			"error": err.Error(),
		})
		g.writeToolError(w, http.StatusServiceUnavailable, &core.ToolError{
			Code:      "API_UNAVAILABLE",
			Message:   "Failed to connect to grocery store API: " + err.Error(),
			Category:  core.CategoryServiceError,
			Retryable: true,
		})
		return
	}
	defer resp.Body.Close()

	// Handle error responses
	if resp.StatusCode != http.StatusOK {
		toolErr := g.handleAPIError(resp)
		telemetry.RecordSpanError(ctx, fmt.Errorf("%s: %s", toolErr.Code, toolErr.Message))
		g.Logger.WarnWithContext(ctx, "API returned error", map[string]interface{}{
			"status":     resp.StatusCode,
			"code":       toolErr.Code,
			"product_id": req.ProductID,
		})
		g.writeToolError(w, resp.StatusCode, toolErr)
		return
	}

	// Parse successful response
	var product Product
	if err := json.NewDecoder(resp.Body).Decode(&product); err != nil {
		g.writeToolError(w, http.StatusInternalServerError, &core.ToolError{
			Code:      "PARSE_ERROR",
			Message:   "Failed to parse API response",
			Category:  core.CategoryServiceError,
			Retryable: true,
		})
		return
	}

	// Add success span event
	telemetry.AddSpanEvent(ctx, "product_retrieved",
		attribute.Int("product_id", req.ProductID),
		attribute.String("name", product.Name),
	)

	g.Logger.InfoWithContext(ctx, "get_product completed", map[string]interface{}{
		"product_id": req.ProductID,
		"name":       product.Name,
	})

	g.writeToolSuccess(w, GetProductResponse{Product: product})
}

// handleCreateCart proxies create_cart to the API
func (g *GroceryTool) handleCreateCart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "create_cart"),
	)

	g.Logger.InfoWithContext(ctx, "Processing create_cart request (proxy to API)", nil)

	// Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_grocery_api",
		attribute.String("path", "/carts"),
		attribute.String("method", "POST"),
	)

	// Make API request
	resp, err := g.makeAPIRequest(ctx, "POST", "/carts", nil)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		g.Logger.ErrorWithContext(ctx, "API request failed", map[string]interface{}{
			"error": err.Error(),
		})
		g.writeToolError(w, http.StatusServiceUnavailable, &core.ToolError{
			Code:      "API_UNAVAILABLE",
			Message:   "Failed to connect to grocery store API: " + err.Error(),
			Category:  core.CategoryServiceError,
			Retryable: true,
		})
		return
	}
	defer resp.Body.Close()

	// Handle error responses
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		toolErr := g.handleAPIError(resp)
		telemetry.RecordSpanError(ctx, fmt.Errorf("%s: %s", toolErr.Code, toolErr.Message))
		g.Logger.WarnWithContext(ctx, "API returned error", map[string]interface{}{
			"status": resp.StatusCode,
			"code":   toolErr.Code,
		})
		g.writeToolError(w, resp.StatusCode, toolErr)
		return
	}

	// Parse successful response
	var cart Cart
	if err := json.NewDecoder(resp.Body).Decode(&cart); err != nil {
		g.writeToolError(w, http.StatusInternalServerError, &core.ToolError{
			Code:      "PARSE_ERROR",
			Message:   "Failed to parse API response",
			Category:  core.CategoryServiceError,
			Retryable: true,
		})
		return
	}

	// Add success span event
	telemetry.AddSpanEvent(ctx, "cart_created",
		attribute.String("cart_id", cart.CartID),
	)

	g.Logger.InfoWithContext(ctx, "create_cart completed", map[string]interface{}{
		"cart_id": cart.CartID,
	})

	g.writeToolSuccess(w, CreateCartResponse{
		CartID:    cart.CartID,
		Message:   "Cart created successfully",
		CreatedAt: cart.Created,
	})
}

// handleAddToCart proxies add_to_cart to the API
func (g *GroceryTool) handleAddToCart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Add span event for Jaeger visibility
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "add_to_cart"),
	)

	g.Logger.InfoWithContext(ctx, "Processing add_to_cart request (proxy to API)", nil)

	var req AddToCartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		g.writeToolError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "INVALID_REQUEST",
			Message:   "Invalid request format: " + err.Error(),
			Category:  core.CategoryInputError,
			Retryable: false,
		})
		return
	}

	// Validate required fields
	if req.CartID == "" {
		g.writeToolError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "MISSING_CART_ID",
			Message:   "cart_id is required",
			Category:  core.CategoryInputError,
			Retryable: false,
			Details: map[string]string{
				"hint": "Use create_cart first to get a cart_id",
			},
		})
		return
	}

	if req.Quantity <= 0 {
		g.writeToolError(w, http.StatusBadRequest, &core.ToolError{
			Code:      "INVALID_QUANTITY",
			Message:   "quantity must be greater than 0",
			Category:  core.CategoryInputError,
			Retryable: false,
			Details: map[string]string{
				"provided_quantity": strconv.Itoa(req.Quantity),
			},
		})
		return
	}

	// Make API request - POST /carts/{cartId}/items
	path := fmt.Sprintf("/carts/%s/items", req.CartID)
	apiReq := map[string]interface{}{
		"productId": req.ProductID,
		"quantity":  req.Quantity,
	}

	// Add span event before API call
	telemetry.AddSpanEvent(ctx, "calling_grocery_api",
		attribute.String("path", path),
		attribute.String("cart_id", req.CartID),
		attribute.Int("product_id", req.ProductID),
		attribute.Int("quantity", req.Quantity),
	)

	resp, err := g.makeAPIRequest(ctx, "POST", path, apiReq)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		g.Logger.ErrorWithContext(ctx, "API request failed", map[string]interface{}{
			"error": err.Error(),
		})
		g.writeToolError(w, http.StatusServiceUnavailable, &core.ToolError{
			Code:      "API_UNAVAILABLE",
			Message:   "Failed to connect to grocery store API: " + err.Error(),
			Category:  core.CategoryServiceError,
			Retryable: true,
		})
		return
	}
	defer resp.Body.Close()

	// Handle error responses
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		toolErr := g.handleAPIError(resp)
		telemetry.RecordSpanError(ctx, fmt.Errorf("%s: %s", toolErr.Code, toolErr.Message))
		g.Logger.WarnWithContext(ctx, "API returned error", map[string]interface{}{
			"status":  resp.StatusCode,
			"code":    toolErr.Code,
			"cart_id": req.CartID,
		})
		g.writeToolError(w, resp.StatusCode, toolErr)
		return
	}

	// Parse successful response
	var item CartItem
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		g.writeToolError(w, http.StatusInternalServerError, &core.ToolError{
			Code:      "PARSE_ERROR",
			Message:   "Failed to parse API response",
			Category:  core.CategoryServiceError,
			Retryable: true,
		})
		return
	}

	// Add success span event
	telemetry.AddSpanEvent(ctx, "item_added_to_cart",
		attribute.String("cart_id", req.CartID),
		attribute.Int("product_id", req.ProductID),
		attribute.Int("quantity", req.Quantity),
		attribute.Int("item_id", item.ID),
	)

	g.Logger.InfoWithContext(ctx, "add_to_cart completed", map[string]interface{}{
		"cart_id":    req.CartID,
		"product_id": req.ProductID,
		"quantity":   req.Quantity,
		"item_id":    item.ID,
	})

	g.writeToolSuccess(w, AddToCartResponse{
		CartID:  req.CartID,
		ItemID:  item.ID,
		Message: fmt.Sprintf("Added %d item(s) to cart", req.Quantity),
	})
}
