package main

import (
	"github.com/truvaagents/truva-g3/core"
)

// ============================================================================
// Grocery Tool - Proxy to Grocery Store API
// ============================================================================
// This tool proxies requests to the grocery-store-api which has error injection
// capabilities for testing circuit breaker and rate limiting scenarios.
//
// The grocery-store-api handles error injection via admin endpoints:
//   - POST /admin/inject-error  - Configure error injection mode
//   - GET  /admin/status        - Get current injection config
//   - POST /admin/reset         - Reset to normal mode
//
// Error Injection Modes (controlled at API level):
//   - "normal":       All requests succeed
//   - "rate_limit":   Return 429 after N requests
//   - "server_error": Return 500 with configurable probability
// ============================================================================

// GroceryTool proxies grocery store capabilities to the real API
type GroceryTool struct {
	*core.BaseTool
	apiBaseURL string
}

// ListProductsRequest represents the input for listing products
type ListProductsRequest struct {
	Category string `json:"category,omitempty"` // Product category filter
	Limit    int    `json:"limit,omitempty"`    // Max results to return
}

// GetProductRequest represents the input for getting a single product
type GetProductRequest struct {
	ProductID int `json:"product_id"` // Product ID to retrieve
}

// CreateCartRequest represents the input for creating a cart (empty)
type CreateCartRequest struct{}

// AddToCartRequest represents the input for adding items to cart
type AddToCartRequest struct {
	CartID    string `json:"cart_id"`    // Cart ID
	ProductID int    `json:"product_id"` // Product to add
	Quantity  int    `json:"quantity"`   // Quantity to add
}

// Product represents a grocery product (matches API response)
type Product struct {
	ID           int     `json:"id"`
	Name         string  `json:"name"`
	Category     string  `json:"category"`
	Manufacturer string  `json:"manufacturer,omitempty"`
	Price        float64 `json:"price"`
	CurrentStock int     `json:"current-stock,omitempty"`
	InStock      bool    `json:"inStock"`
}

// Cart represents a shopping cart
type Cart struct {
	CartID  string     `json:"cartId"`
	Items   []CartItem `json:"items"`
	Created string     `json:"created"`
}

// CartItem represents an item in a cart
type CartItem struct {
	ID        int `json:"id"`
	ProductID int `json:"productId"`
	Quantity  int `json:"quantity"`
}

// ListProductsResponse is the response for listing products
type ListProductsResponse struct {
	Products []Product `json:"products"`
	Count    int       `json:"count"`
	Category string    `json:"category,omitempty"`
}

// GetProductResponse is the response for getting a single product
type GetProductResponse struct {
	Product Product `json:"product"`
}

// CreateCartResponse is the response for creating a cart
type CreateCartResponse struct {
	CartID    string `json:"cart_id"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

// AddToCartResponse is the response for adding items to cart
type AddToCartResponse struct {
	CartID   string  `json:"cart_id"`
	ItemID   int     `json:"item_id"`
	Message  string  `json:"message"`
	Subtotal float64 `json:"subtotal,omitempty"`
}

// NewGroceryTool creates a new grocery tool that proxies to the API
func NewGroceryTool(apiBaseURL string) *GroceryTool {
	tool := &GroceryTool{
		BaseTool:   core.NewTool("grocery-tool"),
		apiBaseURL: apiBaseURL,
	}

	tool.registerCapabilities()
	return tool
}

// registerCapabilities sets up all grocery-related capabilities
func (g *GroceryTool) registerCapabilities() {
	// Capability 1: List Products
	g.RegisterCapability(core.Capability{
		Name:        "list_products",
		Description: "Lists available grocery products from the store.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     g.handleListProducts,
		InputSummary: &core.SchemaSummary{
			OptionalFields: []core.FieldHint{
				{Name: "category", Type: "string", Example: "coffee", Description: "Product category (coffee, dairy, meat-seafood, fresh-produce, candy)"},
				{Name: "limit", Type: "integer", Example: "10", Description: "Maximum number of results"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "products", Type: "array", Description: "Array of product objects with id, name, category, price, inStock fields"},
				{Name: "count", Type: "number", Description: "Number of products returned"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "category", Type: "string", Description: "Category filter that was applied, if any"},
			},
		},
	})

	// Capability 2: Get Product
	g.RegisterCapability(core.Capability{
		Name:        "get_product",
		Description: "Gets details for a specific product by ID from the store.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     g.handleGetProduct,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "product_id", Type: "integer", Example: "1", Description: "Product ID to retrieve"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "product", Type: "object", Description: "Product object with id, name, category, manufacturer, price, current-stock, inStock fields"},
			},
		},
	})

	// Capability 3: Create Cart
	g.RegisterCapability(core.Capability{
		Name:        "create_cart",
		Description: "Creates a new shopping cart in the store. Returns a cart_id for subsequent operations.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     g.handleCreateCart,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "cart_id", Type: "string", Description: "Unique identifier for the created cart"},
				{Name: "message", Type: "string", Description: "Success confirmation message"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "created_at", Type: "string", Description: "Timestamp when the cart was created"},
			},
		},
	})

	// Capability 4: Add to Cart
	g.RegisterCapability(core.Capability{
		Name:        "add_to_cart",
		Description: "Adds a product to an existing cart.",
		InputTypes:  []string{"json"},
		OutputTypes: []string{"json"},
		Handler:     g.handleAddToCart,
		InputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "cart_id", Type: "string", Example: "cart-abc123", Description: "Cart ID from create_cart"},
				{Name: "product_id", Type: "integer", Example: "1", Description: "Product ID to add"},
				{Name: "quantity", Type: "integer", Example: "2", Description: "Quantity to add"},
			},
		},
		OutputSummary: &core.SchemaSummary{
			RequiredFields: []core.FieldHint{
				{Name: "cart_id", Type: "string", Description: "Cart ID the item was added to"},
				{Name: "item_id", Type: "number", Description: "ID of the cart item entry"},
				{Name: "message", Type: "string", Description: "Success confirmation message"},
			},
			OptionalFields: []core.FieldHint{
				{Name: "subtotal", Type: "number", Description: "Cart subtotal after adding the item"},
			},
		},
	})
}
