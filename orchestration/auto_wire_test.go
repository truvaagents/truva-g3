package orchestration

import (
	"testing"
)

func TestAutoWireParameters_ExactNameMatch(t *testing.T) {
	sourceData := map[string]interface{}{
		"lat": 48.85,
		"lon": 2.35,
	}

	targetParams := []Parameter{
		{Name: "lat", Type: "number", Required: true},
		{Name: "lon", Type: "number", Required: true},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(sourceData, targetParams)
	result, unmapped := awResult.Parameters, awResult.Unmapped

	if len(unmapped) != 0 {
		t.Errorf("Expected no unmapped params, got %v", unmapped)
	}

	if result["lat"].(float64) != 48.85 {
		t.Errorf("Expected lat=48.85, got %v", result["lat"])
	}

	if result["lon"].(float64) != 2.35 {
		t.Errorf("Expected lon=2.35, got %v", result["lon"])
	}
}

// TestAutoWireParameters_SemanticMismatch verifies that auto-wiring does NOT
// perform semantic matching. The framework is domain-agnostic; semantic understanding
// (e.g., "latitude" → "lat") is delegated to LLM micro-resolution.
// See PARAMETER_BINDING_FIX.md for the LLM-first design rationale.
func TestAutoWireParameters_SemanticMismatch(t *testing.T) {
	sourceData := map[string]interface{}{
		"latitude":  48.85,
		"longitude": 2.35,
	}

	targetParams := []Parameter{
		{Name: "lat", Type: "number", Required: true},
		{Name: "lon", Type: "number", Required: true},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(sourceData, targetParams)
	result, unmapped := awResult.Parameters, awResult.Unmapped

	// Auto-wiring should NOT match "latitude" to "lat" - that's semantic understanding
	// which is handled by the LLM micro-resolver, not auto-wiring
	if len(unmapped) != 2 {
		t.Errorf("Expected 2 unmapped params (lat, lon), got %v", unmapped)
	}

	if len(result) != 0 {
		t.Errorf("Expected no auto-wired results for semantic mismatch, got %v", result)
	}
}

func TestAutoWireParameters_TypeCoercionFromString(t *testing.T) {
	sourceData := map[string]interface{}{
		"lat": "48.85",
		"lon": "2.35",
	}

	targetParams := []Parameter{
		{Name: "lat", Type: "number", Required: true},
		{Name: "lon", Type: "number", Required: true},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(sourceData, targetParams)
	result, unmapped := awResult.Parameters, awResult.Unmapped

	if len(unmapped) != 0 {
		t.Errorf("Expected no unmapped params, got %v", unmapped)
	}

	if result["lat"].(float64) != 48.85 {
		t.Errorf("Expected lat=48.85, got %v", result["lat"])
	}

	if result["lon"].(float64) != 2.35 {
		t.Errorf("Expected lon=2.35, got %v", result["lon"])
	}
}

func TestAutoWireParameters_CaseInsensitiveMatch(t *testing.T) {
	sourceData := map[string]interface{}{
		"LAT": 48.85,
		"LON": 2.35,
	}

	targetParams := []Parameter{
		{Name: "lat", Type: "number", Required: true},
		{Name: "lon", Type: "number", Required: true},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(sourceData, targetParams)
	result, unmapped := awResult.Parameters, awResult.Unmapped

	if len(unmapped) != 0 {
		t.Errorf("Expected no unmapped params, got %v", unmapped)
	}

	if result["lat"].(float64) != 48.85 {
		t.Errorf("Expected lat=48.85, got %v", result["lat"])
	}
}

func TestAutoWireParameters_UnmappedParams(t *testing.T) {
	sourceData := map[string]interface{}{
		"lat": 48.85,
	}

	targetParams := []Parameter{
		{Name: "lat", Type: "number", Required: true},
		{Name: "lon", Type: "number", Required: true},
		{Name: "unit", Type: "string", Required: false},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(sourceData, targetParams)
	result, unmapped := awResult.Parameters, awResult.Unmapped

	if len(unmapped) != 2 {
		t.Errorf("Expected 2 unmapped params, got %v", unmapped)
	}

	if result["lat"].(float64) != 48.85 {
		t.Errorf("Expected lat=48.85, got %v", result["lat"])
	}
}

func TestAutoWireParameters_NestedDataWrapper(t *testing.T) {
	sourceData := map[string]interface{}{
		"data": map[string]interface{}{
			"lat": 48.85,
			"lon": 2.35,
		},
	}

	targetParams := []Parameter{
		{Name: "lat", Type: "number", Required: true},
		{Name: "lon", Type: "number", Required: true},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(sourceData, targetParams)
	result, unmapped := awResult.Parameters, awResult.Unmapped

	if len(unmapped) != 0 {
		t.Errorf("Expected no unmapped params (should find in nested 'data'), got %v", unmapped)
	}

	if result["lat"].(float64) != 48.85 {
		t.Errorf("Expected lat=48.85, got %v", result["lat"])
	}
}

func TestAutoWireParameters_GeocodingToWeather(t *testing.T) {
	// Real-world test: geocoding response -> weather request
	geocodingResponse := map[string]interface{}{
		"lat":          35.6768601,
		"lon":          139.7638947,
		"display_name": "Tokyo, Japan",
		"type":         "city",
		"importance":   0.8,
	}

	weatherParams := []Parameter{
		{Name: "lat", Type: "number", Required: true, Description: "Latitude"},
		{Name: "lon", Type: "number", Required: true, Description: "Longitude"},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(geocodingResponse, weatherParams)
	result, unmapped := awResult.Parameters, awResult.Unmapped

	if len(unmapped) != 0 {
		t.Errorf("Expected all params wired for geocoding->weather, got unmapped: %v", unmapped)
	}

	lat := result["lat"].(float64)
	lon := result["lon"].(float64)

	if lat != 35.6768601 {
		t.Errorf("Expected lat=35.6768601, got %v", lat)
	}

	if lon != 139.7638947 {
		t.Errorf("Expected lon=139.7638947, got %v", lon)
	}
}

func TestAutoWireParameters_CountryInfoFromGeocoding(t *testing.T) {
	// Test semantic matching for country -> country_name
	geocodingResponse := map[string]interface{}{
		"lat":          48.8566,
		"lon":          2.3522,
		"display_name": "Paris, France",
		"country":      "France",
		"country_code": "FR",
	}

	countryParams := []Parameter{
		{Name: "country", Type: "string", Required: true, Description: "Country name"},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(geocodingResponse, countryParams)
	result, unmapped := awResult.Parameters, awResult.Unmapped

	if len(unmapped) != 0 {
		t.Errorf("Expected country to be wired, got unmapped: %v", unmapped)
	}

	if result["country"].(string) != "France" {
		t.Errorf("Expected country=France, got %v", result["country"])
	}
}

func TestAutoWireParameters_CurrencyFromCountryInfo(t *testing.T) {
	// Test semantic matching for currency
	countryResponse := map[string]interface{}{
		"name":          "France",
		"capital":       "Paris",
		"currency":      "EUR",
		"currency_name": "Euro",
		"population":    67390000,
	}

	currencyParams := []Parameter{
		{Name: "currency", Type: "string", Required: true, Description: "Currency code"},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(countryResponse, currencyParams)
	result, unmapped := awResult.Parameters, awResult.Unmapped

	if len(unmapped) != 0 {
		t.Errorf("Expected currency to be wired, got unmapped: %v", unmapped)
	}

	if result["currency"].(string) != "EUR" {
		t.Errorf("Expected currency=EUR, got %v", result["currency"])
	}
}

func TestCoerceType(t *testing.T) {
	tests := []struct {
		name       string
		input      interface{}
		targetType string
		expected   interface{}
	}{
		{"string to float", "48.85", "number", float64(48.85)},
		{"int to float", 48, "number", float64(48)},
		{"float to float", 48.85, "number", float64(48.85)},
		{"string to int", "42", "integer", int64(42)},
		{"float to int", 42.7, "integer", int64(42)},
		{"bool true", true, "boolean", true},
		{"string true", "true", "boolean", true},
		{"string 1", "1", "boolean", true},
		{"string false", "false", "boolean", false},
		{"int 0", 0, "boolean", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := coerceType(tt.input, tt.targetType)
			if result != tt.expected {
				t.Errorf("coerceType(%v, %s) = %v, want %v", tt.input, tt.targetType, result, tt.expected)
			}
		})
	}
}

// TestAutoWireResult_ResolvedMetadata verifies that AutoWireResult.Resolved
// contains correct per-parameter ParameterResolution entries with Layer,
// MatchType, SourceKey, and Value properly populated.
func TestAutoWireResult_ResolvedMetadata(t *testing.T) {
	sourceData := map[string]interface{}{
		"lat": 48.85,
		"lon": 2.35,
	}

	targetParams := []Parameter{
		{Name: "lat", Type: "number", Required: true},
		{Name: "lon", Type: "number", Required: true},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(sourceData, targetParams)

	// Verify Resolved has 2 entries
	if len(awResult.Resolved) != 2 {
		t.Fatalf("Expected 2 resolved entries, got %d", len(awResult.Resolved))
	}

	// Build lookup by name for order-independent assertions
	resolvedByName := make(map[string]ParameterResolution)
	for _, r := range awResult.Resolved {
		resolvedByName[r.Name] = r
	}

	for _, paramName := range []string{"lat", "lon"} {
		r, ok := resolvedByName[paramName]
		if !ok {
			t.Fatalf("Expected resolved entry for %q", paramName)
		}
		if r.Layer != "auto_wire" {
			t.Errorf("%s: expected Layer=\"auto_wire\", got %q", paramName, r.Layer)
		}
		if r.MatchType != "exact" {
			t.Errorf("%s: expected MatchType=\"exact\", got %q", paramName, r.MatchType)
		}
		if r.SourceKey != paramName {
			t.Errorf("%s: expected SourceKey=%q, got %q", paramName, paramName, r.SourceKey)
		}
	}

	if resolvedByName["lat"].Value.(float64) != 48.85 {
		t.Errorf("lat: expected Value=48.85, got %v", resolvedByName["lat"].Value)
	}
	if resolvedByName["lon"].Value.(float64) != 2.35 {
		t.Errorf("lon: expected Value=2.35, got %v", resolvedByName["lon"].Value)
	}
}

// TestAutoWireResult_MatchType_CaseInsensitive verifies that case-insensitive
// matches produce MatchType="case_insensitive" and track the original source key.
func TestAutoWireResult_MatchType_CaseInsensitive(t *testing.T) {
	sourceData := map[string]interface{}{
		"LAT": 48.85,
		"Lon": 2.35,
	}

	targetParams := []Parameter{
		{Name: "lat", Type: "number", Required: true},
		{Name: "lon", Type: "number", Required: true},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(sourceData, targetParams)

	if len(awResult.Resolved) != 2 {
		t.Fatalf("Expected 2 resolved entries, got %d", len(awResult.Resolved))
	}

	resolvedByName := make(map[string]ParameterResolution)
	for _, r := range awResult.Resolved {
		resolvedByName[r.Name] = r
	}

	// "lat" should match "LAT" via case-insensitive
	latR := resolvedByName["lat"]
	if latR.MatchType != "case_insensitive" {
		t.Errorf("lat: expected MatchType=\"case_insensitive\", got %q", latR.MatchType)
	}
	if latR.SourceKey != "LAT" {
		t.Errorf("lat: expected SourceKey=\"LAT\", got %q", latR.SourceKey)
	}
	if latR.Layer != "auto_wire" {
		t.Errorf("lat: expected Layer=\"auto_wire\", got %q", latR.Layer)
	}

	// "lon" should match "Lon" via case-insensitive
	lonR := resolvedByName["lon"]
	if lonR.MatchType != "case_insensitive" {
		t.Errorf("lon: expected MatchType=\"case_insensitive\", got %q", lonR.MatchType)
	}
	if lonR.SourceKey != "Lon" {
		t.Errorf("lon: expected SourceKey=\"Lon\", got %q", lonR.SourceKey)
	}
}

// TestAutoWireResult_MatchType_NestedExtraction verifies that nested object
// extraction produces MatchType="nested_extraction".
func TestAutoWireResult_MatchType_NestedExtraction(t *testing.T) {
	sourceData := map[string]interface{}{
		"currency": map[string]interface{}{
			"code": "EUR",
			"name": "Euro",
		},
	}

	targetParams := []Parameter{
		{Name: "currency", Type: "string", Required: true},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(sourceData, targetParams)

	if len(awResult.Resolved) != 1 {
		t.Fatalf("Expected 1 resolved entry, got %d", len(awResult.Resolved))
	}

	r := awResult.Resolved[0]
	if r.Name != "currency" {
		t.Errorf("Expected Name=\"currency\", got %q", r.Name)
	}
	if r.MatchType != "nested_extraction" {
		t.Errorf("Expected MatchType=\"nested_extraction\", got %q", r.MatchType)
	}
	if r.SourceKey != "currency" {
		t.Errorf("Expected SourceKey=\"currency\", got %q", r.SourceKey)
	}
	if r.Value.(string) != "EUR" {
		t.Errorf("Expected Value=\"EUR\", got %v", r.Value)
	}
	if r.Layer != "auto_wire" {
		t.Errorf("Expected Layer=\"auto_wire\", got %q", r.Layer)
	}
}

// TestAutoWireResult_DurationUs verifies that DurationUs is tracked and positive.
func TestAutoWireResult_DurationUs(t *testing.T) {
	sourceData := map[string]interface{}{
		"lat": 48.85,
		"lon": 2.35,
	}

	targetParams := []Parameter{
		{Name: "lat", Type: "number", Required: true},
		{Name: "lon", Type: "number", Required: true},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(sourceData, targetParams)

	// DurationUs should be non-negative (may be 0 on very fast runs, but not negative)
	if awResult.DurationUs < 0 {
		t.Errorf("Expected DurationUs >= 0, got %d", awResult.DurationUs)
	}
}

// TestAutoWireResult_UnmappedNotInResolved verifies that unmapped parameters
// do NOT appear in the Resolved list.
func TestAutoWireResult_UnmappedNotInResolved(t *testing.T) {
	sourceData := map[string]interface{}{
		"lat": 48.85,
	}

	targetParams := []Parameter{
		{Name: "lat", Type: "number", Required: true},
		{Name: "lon", Type: "number", Required: true},
		{Name: "unit", Type: "string", Required: false},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(sourceData, targetParams)

	// Should have 1 resolved, 2 unmapped
	if len(awResult.Resolved) != 1 {
		t.Errorf("Expected 1 resolved entry, got %d", len(awResult.Resolved))
	}
	if len(awResult.Unmapped) != 2 {
		t.Errorf("Expected 2 unmapped entries, got %d", len(awResult.Unmapped))
	}

	// The resolved entry should be lat
	if awResult.Resolved[0].Name != "lat" {
		t.Errorf("Expected resolved[0].Name=\"lat\", got %q", awResult.Resolved[0].Name)
	}

	// Unmapped should not appear in resolved
	resolvedNames := make(map[string]bool)
	for _, r := range awResult.Resolved {
		resolvedNames[r.Name] = true
	}
	for _, unmapped := range awResult.Unmapped {
		if resolvedNames[unmapped] {
			t.Errorf("Unmapped param %q should not appear in Resolved", unmapped)
		}
	}
}

// TestAutoWireParameters_ObjectTypeParam verifies that auto-wiring a parameter of type "object"
// where the source data contains a matching map field does NOT panic.
// Regression test for ORCH-005: comparing uncomparable type map[string]interface{}.
func TestAutoWireParameters_ObjectTypeParam(t *testing.T) {
	sourceData := map[string]interface{}{
		"data": map[string]interface{}{
			"earnings": []interface{}{"Q1", "Q2", "Q3"},
			"source":   "Finnhub API",
			"symbol":   "NVDA",
		},
	}

	targetParams := []Parameter{
		{Name: "data", Type: "object", Required: true, Description: "Financial data to analyze"},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(sourceData, targetParams)

	if len(awResult.Unmapped) != 0 {
		t.Errorf("Expected no unmapped params, got %v", awResult.Unmapped)
	}

	// The value should be the original map, passed through as-is
	dataVal, ok := awResult.Parameters["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected data to be map[string]interface{}, got %T", awResult.Parameters["data"])
	}
	if dataVal["symbol"] != "NVDA" {
		t.Errorf("Expected symbol=NVDA, got %v", dataVal["symbol"])
	}

	// matchType should be "exact" (not "nested_extraction") since extractNestedValue
	// returns the map unchanged for type "object"
	if len(awResult.Resolved) != 1 {
		t.Fatalf("Expected 1 resolved entry, got %d", len(awResult.Resolved))
	}
	if awResult.Resolved[0].MatchType != "exact" {
		t.Errorf("Expected MatchType=\"exact\", got %q", awResult.Resolved[0].MatchType)
	}
}

// TestAutoWireParameters_SliceTypeParam verifies that auto-wiring a parameter of type "array"
// where the source data contains a matching slice does NOT panic.
// Part of ORCH-005 fix: slices are also uncomparable in Go.
func TestAutoWireParameters_SliceTypeParam(t *testing.T) {
	sourceData := map[string]interface{}{
		"items": []interface{}{"item1", "item2", "item3"},
	}

	targetParams := []Parameter{
		{Name: "items", Type: "array", Required: true, Description: "List of items"},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(sourceData, targetParams)

	if len(awResult.Unmapped) != 0 {
		t.Errorf("Expected no unmapped params, got %v", awResult.Unmapped)
	}

	items, ok := awResult.Parameters["items"].([]interface{})
	if !ok {
		t.Fatalf("Expected items to be []interface{}, got %T", awResult.Parameters["items"])
	}
	if len(items) != 3 {
		t.Errorf("Expected 3 items, got %d", len(items))
	}
}

// TestAutoWireParameters_NestedExtractionStillWorks verifies that the ORCH-005 fix
// preserves the nested_extraction matchType for map→string extraction.
func TestAutoWireParameters_NestedExtractionStillWorks(t *testing.T) {
	sourceData := map[string]interface{}{
		"currency": map[string]interface{}{
			"code":   "EUR",
			"name":   "Euro",
			"symbol": "€",
		},
	}

	targetParams := []Parameter{
		{Name: "currency", Type: "string", Required: true},
	}

	wirer := NewAutoWirer(nil)
	awResult := wirer.AutoWireParameters(sourceData, targetParams)

	if len(awResult.Resolved) != 1 {
		t.Fatalf("Expected 1 resolved entry, got %d", len(awResult.Resolved))
	}
	r := awResult.Resolved[0]
	if r.MatchType != "nested_extraction" {
		t.Errorf("Expected MatchType=\"nested_extraction\", got %q", r.MatchType)
	}
	if r.Value.(string) != "EUR" {
		t.Errorf("Expected Value=\"EUR\", got %v", r.Value)
	}
}

// TestGetAliases_EmptyByDesign verifies that SemanticAliases is empty.
// The framework is domain-agnostic; all semantic understanding is delegated
// to the LLM micro-resolver. See PARAMETER_BINDING_FIX.md for rationale.
func TestGetAliases_EmptyByDesign(t *testing.T) {
	// Verify the SemanticAliases map is empty (LLM-first design)
	if len(SemanticAliases) != 0 {
		t.Errorf("SemanticAliases should be empty for domain-agnostic design, got %d entries", len(SemanticAliases))
	}

	// getAliases should return empty for any parameter since there are no aliases
	tests := []string{"lat", "latitude", "lon", "longitude", "country", "currency", "foobar"}

	for _, paramName := range tests {
		t.Run(paramName, func(t *testing.T) {
			aliases := getAliases(paramName)
			if len(aliases) != 0 {
				t.Errorf("getAliases(%s) = %v, expected empty slice (LLM-first design)", paramName, aliases)
			}
		})
	}
}
