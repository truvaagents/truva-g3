package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// sendError sends an error response with the given HTTP status code.
// CRITICAL: WriteHeader must be called before Encode per TOOL_DEVELOPMENT_GUIDE Section 6.
func sendError(w http.ResponseWriter, code string, message string, httpStatus int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: false,
		Error: &core.ToolError{
			Code:      code,
			Message:   message,
			Retryable: strings.Contains(code, "UNAVAILABLE"),
		},
	})
}

// handleGetCurrentTime processes get_current_time requests with full telemetry
func (s *SystemTool) handleGetCurrentTime(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	requestID := baggage["request_id"]
	if requestID == "" {
		requestID = uuid.New().String()
	}

	// 3. Set span attributes (request_id FIRST per tracing guide Section 11)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "system-utilities-tool"),
		attribute.String("truvag3.capability", "get_current_time"),
	)

	// 4. Add span event for request start (request_id FIRST per tracing guide Pattern 6)
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "get_current_time"),
	)

	// 5. Log request start (logger nil check per tracing guide)
	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "Processing get_current_time request", map[string]interface{}{
			"operation":  "get_current_time",
			"method":     r.Method,
			"request_id": requestID,
		})
	}

	// 6. Decode request
	var req GetCurrentTimeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "get_current_time",
			"error_type", "decode_error",
		)
		if s.Logger != nil {
			s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "get_current_time",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "INVALID_REQUEST", "Invalid request format", http.StatusBadRequest)
		return
	}

	// 7. Validate required fields
	req.Timezone = strings.TrimSpace(req.Timezone)
	if req.Timezone == "" {
		err := fmt.Errorf("timezone is required")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "get_current_time",
			"error_type", "validation_error",
		)
		if s.Logger != nil {
			s.Logger.WarnWithContext(ctx, "Empty timezone in request", map[string]interface{}{
				"operation":   "get_current_time",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "MISSING_TIMEZONE", "timezone is required", http.StatusBadRequest)
		return
	}

	// Add timezone to span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("timezone.requested", req.Timezone),
	)

	// 8. Process: load timezone and get current time
	loc, err := time.LoadLocation(req.Timezone)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "get_current_time",
			"error_type", "invalid_timezone",
		)
		sendError(w, "INVALID_TIMEZONE", fmt.Sprintf("Unknown timezone: %s", req.Timezone), http.StatusBadRequest)
		return
	}

	now := time.Now().In(loc)
	zone, offset := now.Zone()
	isDST := now.IsDST()

	// Format datetime based on requested format
	var datetime string
	switch strings.ToLower(req.Format) {
	case "unix":
		datetime = fmt.Sprintf("%d", now.Unix())
	case "human":
		datetime = now.Format("Monday, January 2, 2006 at 3:04:05 PM MST")
	case "", "iso8601":
		datetime = now.Format(time.RFC3339)
	default:
		// Treat as Go time layout
		datetime = now.Format(req.Format)
	}

	// Format UTC offset as ±HH:MM
	offsetHours := offset / 3600
	offsetMinutes := (offset % 3600) / 60
	if offsetMinutes < 0 {
		offsetMinutes = -offsetMinutes
	}
	utcOffset := fmt.Sprintf("%+03d:%02d", offsetHours, offsetMinutes)

	response := GetCurrentTimeResponse{
		Timezone:      req.Timezone,
		Datetime:      datetime,
		UnixTimestamp: now.Unix(),
		UTCOffset:     utcOffset,
		IsDST:         isDST,
		Abbreviation:  zone,
	}

	// 9. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("system_utilities.request.duration_ms",
		float64(duration.Milliseconds()),
		"capability", "get_current_time",
	)
	telemetry.Counter("system_utilities.requests.total",
		"capability", "get_current_time",
		"status", "success",
	)

	// 10. Record unified metrics
	telemetry.RecordToolCall("system-utilities-tool", "get_current_time", float64(duration.Milliseconds()), "success")

	// 11. Add completion span event
	telemetry.AddSpanEvent(ctx, "get_current_time_completed",
		attribute.String("request_id", requestID),
		attribute.String("timezone", req.Timezone),
		attribute.String("datetime", datetime),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	// 12. Log completion
	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "get_current_time completed", map[string]interface{}{
			"operation":   "get_current_time",
			"timezone":    req.Timezone,
			"datetime":    datetime,
			"status":      "success",
			"duration_ms": duration.Milliseconds(),
			"request_id":  requestID,
		})
	}

	// 13. Send response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleConvertTimezone processes convert_timezone requests with full telemetry
func (s *SystemTool) handleConvertTimezone(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	baggage := telemetry.GetBaggage(ctx)
	requestID := baggage["request_id"]
	if requestID == "" {
		requestID = uuid.New().String()
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "system-utilities-tool"),
		attribute.String("truvag3.capability", "convert_timezone"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "convert_timezone"),
	)

	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "Processing convert_timezone request", map[string]interface{}{
			"operation":  "convert_timezone",
			"method":     r.Method,
			"request_id": requestID,
		})
	}

	var req ConvertTimezoneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "convert_timezone",
			"error_type", "decode_error",
		)
		if s.Logger != nil {
			s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "convert_timezone",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "INVALID_REQUEST", "Invalid request format", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if req.Datetime == "" || req.FromTimezone == "" || req.ToTimezone == "" {
		err := fmt.Errorf("datetime, from_timezone, and to_timezone are all required")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "convert_timezone",
			"error_type", "validation_error",
		)
		if s.Logger != nil {
			s.Logger.WarnWithContext(ctx, "Missing required fields", map[string]interface{}{
				"operation":   "convert_timezone",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "MISSING_FIELDS", "datetime, from_timezone, and to_timezone are all required", http.StatusBadRequest)
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("timezone.from", req.FromTimezone),
		attribute.String("timezone.to", req.ToTimezone),
	)

	// Parse the input datetime
	t, err := time.Parse(time.RFC3339, req.Datetime)
	if err != nil {
		// Try other common formats
		formats := []string{
			"2006-01-02T15:04:05",
			"2006-01-02 15:04:05",
			"2006-01-02",
		}
		parsed := false
		for _, f := range formats {
			if t, err = time.Parse(f, req.Datetime); err == nil {
				parsed = true
				break
			}
		}
		if !parsed {
			telemetry.RecordSpanError(ctx, fmt.Errorf("invalid datetime format: %s", req.Datetime))
			telemetry.Counter("system_utilities.errors.total",
				"module", "system-utilities-tool",
				"capability", "convert_timezone",
				"error_type", "invalid_datetime",
			)
			sendError(w, "INVALID_DATETIME", fmt.Sprintf("Cannot parse datetime: %s. Use ISO 8601 format.", req.Datetime), http.StatusBadRequest)
			return
		}
	}

	// Load source timezone
	fromLoc, err := time.LoadLocation(req.FromTimezone)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "convert_timezone",
			"error_type", "invalid_timezone",
		)
		if s.Logger != nil {
			s.Logger.WarnWithContext(ctx, "Invalid from_timezone", map[string]interface{}{
				"operation":      "convert_timezone",
				"from_timezone":  req.FromTimezone,
				"error_type":     "invalid_timezone",
				"request_id":     requestID,
				"status":         "failure",
				"duration_ms":    time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "INVALID_TIMEZONE", fmt.Sprintf("Unknown from_timezone: %s", req.FromTimezone), http.StatusBadRequest)
		return
	}

	// Load target timezone
	toLoc, err := time.LoadLocation(req.ToTimezone)
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "convert_timezone",
			"error_type", "invalid_timezone",
		)
		if s.Logger != nil {
			s.Logger.WarnWithContext(ctx, "Invalid to_timezone", map[string]interface{}{
				"operation":    "convert_timezone",
				"to_timezone":  req.ToTimezone,
				"error_type":   "invalid_timezone",
				"request_id":   requestID,
				"status":       "failure",
				"duration_ms":  time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "INVALID_TIMEZONE", fmt.Sprintf("Unknown to_timezone: %s", req.ToTimezone), http.StatusBadRequest)
		return
	}

	// Convert: interpret the parsed time in the source timezone, then convert to target
	fromTime := t.In(fromLoc)
	toTime := fromTime.In(toLoc)

	// Calculate offset difference
	_, fromOffset := fromTime.Zone()
	_, toOffset := toTime.Zone()
	diffSeconds := toOffset - fromOffset
	diffHours := diffSeconds / 3600
	diffMins := (diffSeconds % 3600) / 60
	if diffMins < 0 {
		diffMins = -diffMins
	}
	offsetDiff := fmt.Sprintf("%+03d:%02d", diffHours, diffMins)

	response := ConvertTimezoneResponse{
		Original:         fromTime.Format(time.RFC3339),
		Converted:        toTime.Format(time.RFC3339),
		FromTimezone:     req.FromTimezone,
		ToTimezone:       req.ToTimezone,
		OffsetDifference: offsetDiff,
	}

	duration := time.Since(startTime)
	telemetry.RecordToolCall("system-utilities-tool", "convert_timezone", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "convert_timezone_completed",
		attribute.String("request_id", requestID),
		attribute.String("from", req.FromTimezone),
		attribute.String("to", req.ToTimezone),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "convert_timezone completed", map[string]interface{}{
			"operation":   "convert_timezone",
			"from":        req.FromTimezone,
			"to":          req.ToTimezone,
			"status":      "success",
			"duration_ms": duration.Milliseconds(),
			"request_id":  requestID,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleListTimezones processes list_timezones requests with full telemetry
func (s *SystemTool) handleListTimezones(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	baggage := telemetry.GetBaggage(ctx)
	requestID := baggage["request_id"]
	if requestID == "" {
		requestID = uuid.New().String()
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "system-utilities-tool"),
		attribute.String("truvag3.capability", "list_timezones"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "list_timezones"),
	)

	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "Processing list_timezones request", map[string]interface{}{
			"operation":  "list_timezones",
			"method":     r.Method,
			"request_id": requestID,
		})
	}

	var req ListTimezonesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// For list_timezones, empty body is OK — treat as no filter
		req.Region = ""
	}

	region := strings.TrimSpace(req.Region)

	// Build timezone list
	var zones []string
	regionLabel := "all"
	if region != "" {
		regionLabel = region
		// Case-insensitive region lookup
		for k, v := range timezonesByRegion {
			if strings.EqualFold(k, region) {
				zones = v
				regionLabel = k
				break
			}
		}
		if zones == nil {
			err := fmt.Errorf("unknown region: %s", region)
			telemetry.RecordSpanError(ctx, err)
			telemetry.Counter("system_utilities.errors.total",
				"module", "system-utilities-tool",
				"capability", "list_timezones",
				"error_type", "invalid_region",
			)
			// Return available regions in error message
			var regionNames []string
			for k := range timezonesByRegion {
				regionNames = append(regionNames, k)
			}
			sort.Strings(regionNames)
			sendError(w, "INVALID_REGION",
				fmt.Sprintf("Unknown region: %s. Available regions: %s", region, strings.Join(regionNames, ", ")),
				http.StatusBadRequest)
			return
		}
	} else {
		// All regions
		for _, v := range timezonesByRegion {
			zones = append(zones, v...)
		}
		sort.Strings(zones)
	}

	// Build timezone info with current offsets
	now := time.Now()
	tzInfos := make([]TimezoneInfo, 0, len(zones))
	for _, zoneName := range zones {
		loc, err := time.LoadLocation(zoneName)
		if err != nil {
			continue
		}
		t := now.In(loc)
		abbr, offset := t.Zone()
		hours := offset / 3600
		mins := (offset % 3600) / 60
		if mins < 0 {
			mins = -mins
		}
		tzInfos = append(tzInfos, TimezoneInfo{
			Name:          zoneName,
			CurrentOffset: fmt.Sprintf("%+03d:%02d", hours, mins),
			Abbreviation:  abbr,
		})
	}

	response := ListTimezonesResponse{
		Region:    regionLabel,
		Timezones: tzInfos,
	}

	duration := time.Since(startTime)
	telemetry.RecordToolCall("system-utilities-tool", "list_timezones", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "list_timezones_completed",
		attribute.String("request_id", requestID),
		attribute.String("region", regionLabel),
		attribute.Int("timezone_count", len(tzInfos)),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "list_timezones completed", map[string]interface{}{
			"operation":      "list_timezones",
			"region":         regionLabel,
			"timezone_count": len(tzInfos),
			"status":         "success",
			"duration_ms":    duration.Milliseconds(),
			"request_id":     requestID,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleDateArithmetic processes date_arithmetic requests with full telemetry
func (s *SystemTool) handleDateArithmetic(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	baggage := telemetry.GetBaggage(ctx)
	requestID := baggage["request_id"]
	if requestID == "" {
		requestID = uuid.New().String()
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "system-utilities-tool"),
		attribute.String("truvag3.capability", "date_arithmetic"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "date_arithmetic"),
	)

	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "Processing date_arithmetic request", map[string]interface{}{
			"operation":  "date_arithmetic",
			"method":     r.Method,
			"request_id": requestID,
		})
	}

	var req DateArithmeticRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "date_arithmetic",
			"error_type", "decode_error",
		)
		if s.Logger != nil {
			s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "date_arithmetic",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "INVALID_REQUEST", "Invalid request format", http.StatusBadRequest)
		return
	}

	// Validate required fields
	if req.Date == "" || req.Operation == "" || req.Unit == "" {
		err := fmt.Errorf("date, operation, and unit are required")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "date_arithmetic",
			"error_type", "validation_error",
		)
		if s.Logger != nil {
			s.Logger.WarnWithContext(ctx, "Missing required fields", map[string]interface{}{
				"operation":   "date_arithmetic",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "MISSING_FIELDS", "date, operation, and unit are all required", http.StatusBadRequest)
		return
	}

	// Validate operation
	op := strings.ToLower(req.Operation)
	if op != "add" && op != "subtract" {
		err := fmt.Errorf("invalid operation: %s", req.Operation)
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "date_arithmetic",
			"error_type", "validation_error",
		)
		if s.Logger != nil {
			s.Logger.WarnWithContext(ctx, "Invalid operation value", map[string]interface{}{
				"operation":   "date_arithmetic",
				"value":       req.Operation,
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "INVALID_OPERATION", "operation must be 'add' or 'subtract'", http.StatusBadRequest)
		return
	}

	// Validate unit
	validUnits := map[string]bool{
		"days": true, "hours": true, "minutes": true,
		"weeks": true, "months": true, "years": true,
	}
	unit := strings.ToLower(req.Unit)
	if !validUnits[unit] {
		err := fmt.Errorf("invalid unit: %s", req.Unit)
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "date_arithmetic",
			"error_type", "validation_error",
		)
		if s.Logger != nil {
			s.Logger.WarnWithContext(ctx, "Invalid unit value", map[string]interface{}{
				"operation":   "date_arithmetic",
				"value":       req.Unit,
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "INVALID_UNIT", "unit must be one of: days, hours, minutes, weeks, months, years", http.StatusBadRequest)
		return
	}

	// Parse the date
	var t time.Time
	var parseErr error
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, parseErr = time.Parse(f, req.Date); parseErr == nil {
			break
		}
	}
	if parseErr != nil {
		telemetry.RecordSpanError(ctx, parseErr)
		sendError(w, "INVALID_DATE", fmt.Sprintf("Cannot parse date: %s", req.Date), http.StatusBadRequest)
		return
	}

	// Apply timezone if provided
	if req.Timezone != "" {
		loc, err := time.LoadLocation(req.Timezone)
		if err != nil {
			telemetry.RecordSpanError(ctx, err)
			sendError(w, "INVALID_TIMEZONE", fmt.Sprintf("Unknown timezone: %s", req.Timezone), http.StatusBadRequest)
			return
		}
		t = t.In(loc)
	}

	original := t

	// Perform arithmetic
	value := req.Value
	if op == "subtract" {
		value = -value
	}

	switch unit {
	case "years":
		t = t.AddDate(value, 0, 0)
	case "months":
		t = t.AddDate(0, value, 0)
	case "weeks":
		t = t.AddDate(0, 0, value*7)
	case "days":
		t = t.AddDate(0, 0, value)
	case "hours":
		t = t.Add(time.Duration(value) * time.Hour)
	case "minutes":
		t = t.Add(time.Duration(value) * time.Minute)
	}

	// Calculate days between
	daysBetween := int(t.Sub(original).Hours() / 24)
	if daysBetween < 0 {
		daysBetween = -daysBetween
	}

	response := DateArithmeticResponse{
		OriginalDate: original.Format(time.RFC3339),
		ResultDate:   t.Format(time.RFC3339),
		Operation:    op,
		Value:        req.Value,
		Unit:         unit,
		DaysBetween:  daysBetween,
	}

	duration := time.Since(startTime)
	telemetry.RecordToolCall("system-utilities-tool", "date_arithmetic", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "date_arithmetic_completed",
		attribute.String("request_id", requestID),
		attribute.String("operation", op),
		attribute.Int("value", req.Value),
		attribute.String("unit", unit),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "date_arithmetic completed", map[string]interface{}{
			"operation":   "date_arithmetic",
			"op":          op,
			"value":       req.Value,
			"unit":        unit,
			"status":      "success",
			"duration_ms": duration.Milliseconds(),
			"request_id":  requestID,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleExecuteCommand processes execute_command requests with full telemetry
func (s *SystemTool) handleExecuteCommand(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	baggage := telemetry.GetBaggage(ctx)
	requestID := baggage["request_id"]
	if requestID == "" {
		requestID = uuid.New().String()
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "system-utilities-tool"),
		attribute.String("truvag3.capability", "execute_command"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "execute_command"),
	)

	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "Processing execute_command request", map[string]interface{}{
			"operation":  "execute_command",
			"method":     r.Method,
			"request_id": requestID,
		})
	}

	var req ExecuteCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "execute_command",
			"error_type", "decode_error",
		)
		if s.Logger != nil {
			s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "execute_command",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "INVALID_REQUEST", "Invalid request format", http.StatusBadRequest)
		return
	}

	// Validate required field
	req.Command = strings.TrimSpace(req.Command)
	if req.Command == "" {
		err := fmt.Errorf("command is required")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "execute_command",
			"error_type", "validation_error",
		)
		if s.Logger != nil {
			s.Logger.WarnWithContext(ctx, "Empty command in request", map[string]interface{}{
				"operation":   "execute_command",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "MISSING_COMMAND", "command is required", http.StatusBadRequest)
		return
	}

	// Set timeout (default 30s, max 300s)
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30
	}
	if timeout > 300 {
		timeout = 300
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("command.input", req.Command),
		attribute.Int("command.timeout", timeout),
	)

	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "Executing command", map[string]interface{}{
			"operation":  "execute_command",
			"command":    req.Command,
			"timeout":    timeout,
			"request_id": requestID,
		})
	}

	// Add span event before execution
	telemetry.AddSpanEvent(ctx, "command_executing",
		attribute.String("request_id", requestID),
		attribute.String("command", req.Command),
		attribute.Int("timeout_seconds", timeout),
	)

	// Execute command with timeout
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "sh", "-c", req.Command)

	// Set working directory if provided
	if req.WorkingDirectory != "" {
		cmd.Dir = req.WorkingDirectory
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmdStartTime := time.Now()
	err := cmd.Run()
	cmdDuration := time.Since(cmdStartTime)

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else if execCtx.Err() == context.DeadlineExceeded {
			exitCode = 124 // Standard timeout exit code
			stderr.WriteString(fmt.Sprintf("\nCommand timed out after %d seconds", timeout))
		} else {
			exitCode = 1
			if stderr.Len() == 0 {
				stderr.WriteString(err.Error())
			}
		}
	}

	response := ExecuteCommandResponse{
		Command:    req.Command,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		ExitCode:   exitCode,
		DurationMs: cmdDuration.Milliseconds(),
	}

	// Record metrics
	duration := time.Since(startTime)
	status := "success"
	if exitCode != 0 {
		status = "error"
	}

	telemetry.Histogram("system_utilities.command.duration_ms",
		float64(cmdDuration.Milliseconds()),
		"capability", "execute_command",
	)
	telemetry.Counter("system_utilities.requests.total",
		"capability", "execute_command",
		"status", status,
	)
	telemetry.RecordToolCall("system-utilities-tool", "execute_command", float64(duration.Milliseconds()), status)

	telemetry.AddSpanEvent(ctx, "command_completed",
		attribute.String("request_id", requestID),
		attribute.Int("exit_code", exitCode),
		attribute.Int64("cmd_duration_ms", cmdDuration.Milliseconds()),
		attribute.Int("stdout_bytes", stdout.Len()),
		attribute.Int("stderr_bytes", stderr.Len()),
	)

	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "execute_command completed", map[string]interface{}{
			"operation":       "execute_command",
			"command":         req.Command,
			"exit_code":       exitCode,
			"cmd_duration_ms": cmdDuration.Milliseconds(),
			"stdout_bytes":    stdout.Len(),
			"stderr_bytes":    stderr.Len(),
			"status":          status,
			"duration_ms":     duration.Milliseconds(),
			"request_id":      requestID,
		})
	}

	// Always return 200 — the exit_code in the response body indicates command success/failure
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleGenerateID processes generate_id requests with full telemetry
func (s *SystemTool) handleGenerateID(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	baggage := telemetry.GetBaggage(ctx)
	requestID := baggage["request_id"]
	if requestID == "" {
		requestID = uuid.New().String()
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "system-utilities-tool"),
		attribute.String("truvag3.capability", "generate_id"),
	)

	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "generate_id"),
	)

	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "Processing generate_id request", map[string]interface{}{
			"operation":  "generate_id",
			"method":     r.Method,
			"request_id": requestID,
		})
	}

	var req GenerateIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Empty body is OK — use defaults
		req.Type = "uuid"
		req.Count = 1
	}

	// Apply defaults
	if req.Type == "" {
		req.Type = "uuid"
	}
	req.Type = strings.ToLower(req.Type)

	if req.Count <= 0 {
		req.Count = 1
	}
	if req.Count > 100 {
		req.Count = 100
	}

	// Validate type
	validTypes := map[string]bool{"uuid": true, "ulid": true, "nanoid": true}
	if !validTypes[req.Type] {
		err := fmt.Errorf("invalid id type: %s", req.Type)
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "generate_id",
			"error_type", "validation_error",
		)
		sendError(w, "INVALID_TYPE", "type must be one of: uuid, ulid, nanoid", http.StatusBadRequest)
		return
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("id.type", req.Type),
		attribute.Int("id.count", req.Count),
	)

	// Generate IDs
	ids := make([]string, 0, req.Count)
	for i := 0; i < req.Count; i++ {
		var id string
		switch req.Type {
		case "uuid":
			id = uuid.New().String()
		case "ulid":
			id = generateULID()
		case "nanoid":
			id = generateNanoid(21)
		}
		ids = append(ids, id)
	}

	response := GenerateIDResponse{
		Type: req.Type,
		IDs:  ids,
	}

	duration := time.Since(startTime)
	telemetry.RecordToolCall("system-utilities-tool", "generate_id", float64(duration.Milliseconds()), "success")

	telemetry.AddSpanEvent(ctx, "generate_id_completed",
		attribute.String("request_id", requestID),
		attribute.String("type", req.Type),
		attribute.Int("count", req.Count),
		attribute.Int64("duration_ms", duration.Milliseconds()),
	)

	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "generate_id completed", map[string]interface{}{
			"operation":   "generate_id",
			"type":        req.Type,
			"count":       req.Count,
			"status":      "success",
			"duration_ms": duration.Milliseconds(),
			"request_id":  requestID,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    response,
	})
}

// handleStealthBrowser processes stealth_browser requests with full telemetry.
// It launches a headless Chromium via Playwright + stealth plugin (Node.js) and
// returns page content, optional screenshot, and optional JS execution result.
func (s *SystemTool) handleStealthBrowser(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Trace context
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Request ID from baggage
	baggage := telemetry.GetBaggage(ctx)
	requestID := baggage["request_id"]
	if requestID == "" {
		requestID = uuid.New().String()
	}

	// 3. Span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "system-utilities-tool"),
		attribute.String("truvag3.capability", "stealth_browser"),
	)

	// 4. Span event: request received
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "stealth_browser"),
	)

	// 5. Log request start
	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "Processing stealth_browser request", map[string]interface{}{
			"operation":  "stealth_browser",
			"method":     r.Method,
			"request_id": requestID,
		})
	}

	// 6. Decode request
	var req StealthBrowserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "stealth_browser",
			"error_type", "decode_error",
		)
		if s.Logger != nil {
			s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "stealth_browser",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "INVALID_REQUEST", "Invalid request format", http.StatusBadRequest)
		return
	}

	// 7. Validate required field
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		err := fmt.Errorf("url is required")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "stealth_browser",
			"error_type", "validation_error",
		)
		if s.Logger != nil {
			s.Logger.WarnWithContext(ctx, "Empty url in request", map[string]interface{}{
				"operation":   "stealth_browser",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "MISSING_URL", "url is required", http.StatusBadRequest)
		return
	}

	// Validate URL has a protocol
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		sendError(w, "INVALID_URL", "url must start with http:// or https://", http.StatusBadRequest)
		return
	}

	// 8. Apply defaults and limits
	if req.ExtractContent == "" {
		req.ExtractContent = "text"
	}
	extractContent := strings.ToLower(req.ExtractContent)
	if extractContent != "text" && extractContent != "html" && extractContent != "both" {
		extractContent = "text"
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 60
	}
	if timeout > 120 {
		timeout = 120
	}

	telemetry.SetSpanAttributes(ctx,
		attribute.String("browser.url", req.URL),
		attribute.Int("browser.timeout", timeout),
		attribute.Bool("browser.screenshot", req.Screenshot),
		attribute.String("browser.extract", extractContent),
	)

	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "Launching stealth browser", map[string]interface{}{
			"operation":       "stealth_browser",
			"url":             req.URL,
			"timeout":         timeout,
			"screenshot":      req.Screenshot,
			"extract_content": extractContent,
			"request_id":      requestID,
		})
	}

	// 9. Span event: browser launching
	telemetry.AddSpanEvent(ctx, "browser_launching",
		attribute.String("request_id", requestID),
		attribute.String("url", req.URL),
		attribute.Int("timeout_seconds", timeout),
	)

	// 10. Build the Node.js script that uses playwright-extra + stealth
	script := buildPlaywrightScript(req.URL, req.WaitFor, extractContent, req.Screenshot, timeout, req.JavaScript, req.UserAgent)

	// 11. Execute the Node.js script with a timeout slightly beyond the page timeout
	execTimeout := time.Duration(timeout+15) * time.Second
	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "node", "-e", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmdStartTime := time.Now()
	err := cmd.Run()
	cmdDuration := time.Since(cmdStartTime)

	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "stealth_browser",
			"error_type", "browser_error",
		)

		errMsg := stderr.String()
		if execCtx.Err() == context.DeadlineExceeded {
			errMsg = fmt.Sprintf("Browser timed out after %d seconds", timeout)
		}
		if errMsg == "" {
			errMsg = err.Error()
		}

		if s.Logger != nil {
			s.Logger.ErrorWithContext(ctx, "Stealth browser execution failed", map[string]interface{}{
				"operation":       "stealth_browser",
				"url":             req.URL,
				"error":           errMsg,
				"error_type":      "browser_error",
				"cmd_duration_ms": cmdDuration.Milliseconds(),
				"request_id":      requestID,
				"status":          "failure",
				"duration_ms":     time.Since(startTime).Milliseconds(),
			})
		}

		sendError(w, "BROWSER_ERROR", fmt.Sprintf("Browser execution failed: %s", errMsg), http.StatusInternalServerError)
		return
	}

	// 12. Parse the JSON output from the Node.js script
	var result StealthBrowserResponse
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "stealth_browser",
			"error_type", "parse_error",
		)
		if s.Logger != nil {
			s.Logger.ErrorWithContext(ctx, "Failed to parse browser output", map[string]interface{}{
				"operation":   "stealth_browser",
				"error":       err.Error(),
				"error_type":  "parse_error",
				"stdout_len":  stdout.Len(),
				"stderr":      stderr.String(),
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "PARSE_ERROR", "Failed to parse browser output", http.StatusInternalServerError)
		return
	}

	result.DurationMs = cmdDuration.Milliseconds()

	// 13. Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("system_utilities.browser.duration_ms",
		float64(cmdDuration.Milliseconds()),
		"capability", "stealth_browser",
	)
	telemetry.Counter("system_utilities.requests.total",
		"capability", "stealth_browser",
		"status", "success",
	)
	telemetry.RecordToolCall("system-utilities-tool", "stealth_browser", float64(duration.Milliseconds()), "success")

	// 14. Span event: completed
	telemetry.AddSpanEvent(ctx, "stealth_browser_completed",
		attribute.String("request_id", requestID),
		attribute.String("url", result.URL),
		attribute.String("title", result.Title),
		attribute.Int("status_code", result.StatusCode),
		attribute.Int64("cmd_duration_ms", cmdDuration.Milliseconds()),
	)

	// 15. Log completion
	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "stealth_browser completed", map[string]interface{}{
			"operation":       "stealth_browser",
			"url":             result.URL,
			"title":           result.Title,
			"status_code":     result.StatusCode,
			"has_screenshot":  result.ScreenshotBase64 != "",
			"has_js_result":   result.JSResult != "",
			"cmd_duration_ms": cmdDuration.Milliseconds(),
			"status":          "success",
			"duration_ms":     duration.Milliseconds(),
			"request_id":      requestID,
		})
	}

	// 16. Send response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})
}

// buildPlaywrightScript generates a Node.js script that uses playwright-extra
// with the stealth plugin to navigate a URL and extract content.
func buildPlaywrightScript(url, waitFor, extractContent string, screenshot bool, timeout int, javascript, userAgent string) string {
	timeoutMs := timeout * 1000

	// Escape strings for safe embedding in JS
	escapeJS := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `'`, `\'`)
		s = strings.ReplaceAll(s, "\n", `\n`)
		s = strings.ReplaceAll(s, "\r", `\r`)
		return s
	}

	// Build optional sections
	var waitForBlock string
	if waitFor != "" {
		waitForBlock = fmt.Sprintf(`
    await page.waitForSelector('%s', { timeout: %d });`, escapeJS(waitFor), timeoutMs)
	}

	var contextOptions string
	if userAgent != "" {
		contextOptions = fmt.Sprintf(`{ userAgent: '%s' }`, escapeJS(userAgent))
	} else {
		contextOptions = `{}`
	}

	var extractBlock string
	switch extractContent {
	case "html":
		extractBlock = `
    result.html_content = await page.content();`
	case "both":
		extractBlock = `
    result.text_content = await page.evaluate(() => document.body.innerText);
    result.html_content = await page.content();`
	default: // "text"
		extractBlock = `
    result.text_content = await page.evaluate(() => document.body.innerText);`
	}

	var screenshotBlock string
	if screenshot {
		screenshotBlock = `
    const screenshotBuf = await page.screenshot({ fullPage: true });
    result.screenshot_base64 = screenshotBuf.toString('base64');`
	}

	var jsBlock string
	if javascript != "" {
		jsBlock = fmt.Sprintf(`
    try {
      const jsResult = await page.evaluate(async () => { %s });
      result.js_result = String(jsResult);
    } catch (jsErr) {
      result.js_result = 'JS_ERROR: ' + jsErr.message;
    }`, javascript)
	}

	script := fmt.Sprintf(`
const { chromium } = require('playwright-extra');
const stealth = require('puppeteer-extra-plugin-stealth');
chromium.use(stealth());

(async () => {
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      args: ['--no-sandbox', '--disable-setuid-sandbox', '--disable-dev-shm-usage']
    });
    const context = await browser.newContext(%s);
    const page = await context.newPage();
    const response = await page.goto('%s', {
      waitUntil: 'domcontentloaded',
      timeout: %d
    });
    %s
    const result = {
      url: page.url(),
      title: await page.title(),
      status_code: response ? response.status() : 0
    };
    %s%s%s
    console.log(JSON.stringify(result));
  } catch (err) {
    console.error(err.message);
    process.exit(1);
  } finally {
    if (browser) await browser.close();
  }
})();
`, contextOptions, escapeJS(url), timeoutMs, waitForBlock, extractBlock, screenshotBlock, jsBlock)

	return script
}

// handleBrowserTest processes browser_test requests with full telemetry.
// It launches a headless Chromium via Playwright + stealth plugin and executes
// an ordered sequence of test actions, returning per-step pass/fail results.
func (s *SystemTool) handleBrowserTest(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// Step 1: Trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	// Step 2: Request ID from baggage
	baggage := telemetry.GetBaggage(ctx)
	requestID := baggage["request_id"]
	if requestID == "" {
		requestID = uuid.New().String()
	}

	// Step 3: Span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "system-utilities-tool"),
		attribute.String("truvag3.capability", "browser_test"),
	)

	// Step 4: Span event — request received
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "browser_test"),
	)

	// Step 5: Log request start
	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "Processing browser_test request", map[string]interface{}{
			"operation":  "browser_test",
			"method":     r.Method,
			"request_id": requestID,
		})
	}

	// Step 6: Decode request
	var req BrowserTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "browser_test",
			"error_type", "decode_error",
		)
		if s.Logger != nil {
			s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "browser_test",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "INVALID_REQUEST", "Invalid request format", http.StatusBadRequest)
		return
	}

	// Step 7: Validate required fields — URL
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		err := fmt.Errorf("url is required")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "browser_test",
			"error_type", "validation_error",
		)
		if s.Logger != nil {
			s.Logger.WarnWithContext(ctx, "Empty url in request", map[string]interface{}{
				"operation":   "browser_test",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "MISSING_URL", "url is required", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		err := fmt.Errorf("url must start with http:// or https://")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "browser_test",
			"error_type", "validation_error",
		)
		sendError(w, "INVALID_URL", "url must start with http:// or https://", http.StatusBadRequest)
		return
	}

	// Validate required fields — actions
	if len(req.Actions) == 0 {
		err := fmt.Errorf("actions array is required and must not be empty")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "browser_test",
			"error_type", "validation_error",
		)
		if s.Logger != nil {
			s.Logger.WarnWithContext(ctx, "Empty actions array", map[string]interface{}{
				"operation":   "browser_test",
				"error_type":  "validation_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "MISSING_ACTIONS", "actions array is required and must not be empty", http.StatusBadRequest)
		return
	}

	const maxActions = 200
	if len(req.Actions) > maxActions {
		err := fmt.Errorf("actions array exceeds maximum of %d steps (got %d)", maxActions, len(req.Actions))
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "browser_test",
			"error_type", "validation_error",
		)
		if s.Logger != nil {
			s.Logger.WarnWithContext(ctx, "Actions array too large", map[string]interface{}{
				"operation":    "browser_test",
				"error_type":   "validation_error",
				"action_count": len(req.Actions),
				"max_actions":  maxActions,
				"request_id":   requestID,
				"status":       "failure",
				"duration_ms":  time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "TOO_MANY_ACTIONS", fmt.Sprintf("actions array exceeds maximum of %d steps", maxActions), http.StatusBadRequest)
		return
	}

	// Step 8: Apply defaults and clamp limits
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 120
	}
	if timeout > 300 {
		timeout = 300
	}

	viewportWidth := 1280
	viewportHeight := 720
	if req.Viewport != nil {
		if req.Viewport.Width > 0 {
			viewportWidth = req.Viewport.Width
		}
		if req.Viewport.Height > 0 {
			viewportHeight = req.Viewport.Height
		}
	}

	// Step 9: Set span attributes with request details
	telemetry.SetSpanAttributes(ctx,
		attribute.String("browser.url", req.URL),
		attribute.Int("browser.timeout", timeout),
		attribute.Int("browser.action_count", len(req.Actions)),
		attribute.String("browser.viewport", fmt.Sprintf("%dx%d", viewportWidth, viewportHeight)),
	)

	// Step 10: Log launch details
	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "Launching browser test", map[string]interface{}{
			"operation":    "browser_test",
			"url":          req.URL,
			"timeout":      timeout,
			"action_count": len(req.Actions),
			"viewport":     fmt.Sprintf("%dx%d", viewportWidth, viewportHeight),
			"request_id":   requestID,
		})
	}

	// Step 11: Span event — browser launching
	telemetry.AddSpanEvent(ctx, "browser_test_launching",
		attribute.String("request_id", requestID),
		attribute.String("url", req.URL),
		attribute.Int("action_count", len(req.Actions)),
		attribute.Int("timeout_seconds", timeout),
	)

	// Step 12: Build the Node.js Playwright test script
	script := buildPlaywrightTestScript(req.URL, req.Actions, timeout, viewportWidth, viewportHeight)

	// Step 13: Execute the Node.js script with timeout buffer
	execTimeout := time.Duration(timeout+15) * time.Second
	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "node", "-e", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmdStartTime := time.Now()
	err := cmd.Run()
	cmdDuration := time.Since(cmdStartTime)

	// Step 14: Handle execution errors
	if err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "browser_test",
			"error_type", "browser_error",
		)

		errMsg := stderr.String()
		if execCtx.Err() == context.DeadlineExceeded {
			errMsg = fmt.Sprintf("Browser test timed out after %d seconds", timeout)
		}
		if errMsg == "" {
			errMsg = err.Error()
		}

		if s.Logger != nil {
			s.Logger.ErrorWithContext(ctx, "Browser test execution failed", map[string]interface{}{
				"operation":       "browser_test",
				"url":             req.URL,
				"error":           errMsg,
				"error_type":      "browser_error",
				"cmd_duration_ms": cmdDuration.Milliseconds(),
				"request_id":      requestID,
				"status":          "failure",
				"duration_ms":     time.Since(startTime).Milliseconds(),
			})
		}

		sendError(w, "BROWSER_ERROR", fmt.Sprintf("Browser test execution failed: %s", errMsg), http.StatusInternalServerError)
		return
	}

	// Step 15: Parse the JSON output
	var result BrowserTestResponse
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "browser_test",
			"error_type", "parse_error",
		)
		if s.Logger != nil {
			s.Logger.ErrorWithContext(ctx, "Failed to parse browser test output", map[string]interface{}{
				"operation":   "browser_test",
				"error":       err.Error(),
				"error_type":  "parse_error",
				"stdout_len":  stdout.Len(),
				"stderr":      stderr.String(),
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "PARSE_ERROR", "Failed to parse browser test output", http.StatusInternalServerError)
		return
	}

	// Step 16: Set duration from actual execution
	result.DurationMs = cmdDuration.Milliseconds()

	// Step 17: Record success metrics
	duration := time.Since(startTime)
	telemetry.Histogram("system_utilities.browser.duration_ms",
		float64(cmdDuration.Milliseconds()),
		"capability", "browser_test",
	)
	telemetry.Counter("system_utilities.requests.total",
		"capability", "browser_test",
		"status", "success",
	)
	telemetry.RecordToolCall("system-utilities-tool", "browser_test", float64(duration.Milliseconds()), "success")

	// Step 18: Span event — completed
	telemetry.AddSpanEvent(ctx, "browser_test_completed",
		attribute.String("request_id", requestID),
		attribute.String("url", result.URL),
		attribute.Bool("passed", result.Passed),
		attribute.Int("total_steps", result.TotalSteps),
		attribute.Int("passed_steps", result.PassedSteps),
		attribute.Int("failed_steps", result.FailedSteps),
		attribute.Int64("cmd_duration_ms", cmdDuration.Milliseconds()),
	)

	// Step 19: Log completion
	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "browser_test completed", map[string]interface{}{
			"operation":       "browser_test",
			"url":             result.URL,
			"passed":          result.Passed,
			"total_steps":     result.TotalSteps,
			"passed_steps":    result.PassedSteps,
			"failed_steps":    result.FailedSteps,
			"cmd_duration_ms": cmdDuration.Milliseconds(),
			"status":          "success",
			"duration_ms":     duration.Milliseconds(),
			"request_id":      requestID,
		})
	}

	// Step 20: Send response
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})
}

// handleWait processes wait requests with full telemetry
func (s *SystemTool) handleWait(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	ctx := r.Context()

	// 1. Extract trace context for response headers
	tc := telemetry.GetTraceContext(ctx)
	if tc.TraceID != "" {
		w.Header().Set("X-Trace-ID", tc.TraceID)
		w.Header().Set("X-Span-ID", tc.SpanID)
	}

	// 2. Read upstream baggage for correlation
	baggage := telemetry.GetBaggage(ctx)
	requestID := baggage["request_id"]
	if requestID == "" {
		requestID = uuid.New().String()
	}

	// 3. Set span attributes (request_id FIRST per tracing guide Section 11)
	telemetry.SetSpanAttributes(ctx,
		attribute.String("request_id", requestID),
		attribute.String("truvag3.tool.name", "system-utilities-tool"),
		attribute.String("truvag3.capability", "wait"),
	)

	// 4. Add span event for request start (request_id FIRST per tracing guide Pattern 6)
	telemetry.AddSpanEvent(ctx, "request_received",
		attribute.String("request_id", requestID),
		attribute.String("method", r.Method),
		attribute.String("path", r.URL.Path),
		attribute.String("operation", "wait"),
	)

	// 5. Log request start (logger nil check per tracing guide)
	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "Processing wait request", map[string]interface{}{
			"operation":  "wait",
			"method":     r.Method,
			"path":       r.URL.Path,
			"request_id": requestID,
		})
	}

	// 6. Decode request
	var req WaitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "wait",
			"error_type", "decode_error",
		)
		if s.Logger != nil {
			s.Logger.ErrorWithContext(ctx, "Failed to decode request", map[string]interface{}{
				"operation":   "wait",
				"error":       err.Error(),
				"error_type":  "decode_error",
				"request_id":  requestID,
				"status":      "failure",
				"duration_ms": time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "INVALID_REQUEST", "Invalid request format", http.StatusBadRequest)
		return
	}

	// 7. Validate required field
	if req.DurationSeconds <= 0 {
		err := fmt.Errorf("duration_seconds must be > 0")
		telemetry.RecordSpanError(ctx, err)
		telemetry.Counter("system_utilities.errors.total",
			"module", "system-utilities-tool",
			"capability", "wait",
			"error_type", "validation_error",
		)
		if s.Logger != nil {
			s.Logger.WarnWithContext(ctx, "Invalid duration in wait request", map[string]interface{}{
				"operation":        "wait",
				"error_type":       "validation_error",
				"duration_seconds": req.DurationSeconds,
				"request_id":       requestID,
				"status":           "failure",
				"duration_ms":      time.Since(startTime).Milliseconds(),
			})
		}
		sendError(w, "INVALID_DURATION", "duration_seconds must be > 0", http.StatusBadRequest)
		return
	}

	// 8. Clamp to max
	const maxWaitSeconds = 120
	requested := req.DurationSeconds
	duration := requested
	if duration > maxWaitSeconds {
		duration = maxWaitSeconds
		if s.Logger != nil {
			s.Logger.WarnWithContext(ctx, "Wait duration clamped to max", map[string]interface{}{
				"operation":         "wait",
				"requested_seconds": requested,
				"max_seconds":       maxWaitSeconds,
				"request_id":        requestID,
			})
		}
	}

	// 9. Set wait-specific span attributes
	telemetry.SetSpanAttributes(ctx,
		attribute.Int("wait.requested_seconds", requested),
		attribute.Int("wait.duration_seconds", duration),
		attribute.String("wait.reason", req.Reason),
	)

	// 10. Add span event for wait start (request_id FIRST per Pattern 6)
	telemetry.AddSpanEvent(ctx, "wait_started",
		attribute.String("request_id", requestID),
		attribute.Int("duration_seconds", duration),
	)

	// 11. Wait with cancellation support
	startedAt := time.Now()
	cancelled := false
	timer := time.NewTimer(time.Duration(duration) * time.Second)
	select {
	case <-timer.C:
	case <-ctx.Done():
		cancelled = true
		timer.Stop()
	}
	endedAt := time.Now()
	actualSeconds := int(endedAt.Sub(startedAt).Round(time.Second).Seconds())

	telemetry.SetSpanAttributes(ctx, attribute.Bool("wait.cancelled", cancelled))

	// 12. Capability-specific metrics
	telemetry.Counter("system_utilities.wait.total",
		"module", "system-utilities-tool",
		"cancelled", fmt.Sprintf("%t", cancelled),
	)
	telemetry.Histogram("system_utilities.wait.duration_ms",
		float64(endedAt.Sub(startedAt).Milliseconds()),
		"module", "system-utilities-tool",
		"cancelled", fmt.Sprintf("%t", cancelled),
	)

	// 13. Generic metrics (matches every sibling handler)
	handlerDuration := time.Since(startTime)
	telemetry.Histogram("system_utilities.request.duration_ms",
		float64(handlerDuration.Milliseconds()),
		"capability", "wait",
	)
	telemetry.Counter("system_utilities.requests.total",
		"capability", "wait",
		"status", "success",
	)
	telemetry.RecordToolCall("system-utilities-tool", "wait",
		float64(handlerDuration.Milliseconds()), "success")

	// 14. Build response
	result := WaitResponse{
		RequestedSeconds: requested,
		DurationSeconds:  actualSeconds,
		Reason:           req.Reason,
		StartedAt:        startedAt.UTC().Format(time.RFC3339),
		EndedAt:          endedAt.UTC().Format(time.RFC3339),
		Cancelled:        cancelled,
	}

	// 15. Add completion span event (request_id FIRST per Pattern 6)
	telemetry.AddSpanEvent(ctx, "wait_completed",
		attribute.String("request_id", requestID),
		attribute.Bool("cancelled", cancelled),
		attribute.Int("actual_seconds", actualSeconds),
		attribute.Int64("duration_ms", handlerDuration.Milliseconds()),
	)

	// 16. Log completion
	if s.Logger != nil {
		s.Logger.InfoWithContext(ctx, "wait request completed", map[string]interface{}{
			"operation":         "wait",
			"requested_seconds": requested,
			"actual_seconds":    actualSeconds,
			"cancelled":         cancelled,
			"request_id":        requestID,
			"status":            "success",
			"duration_ms":       handlerDuration.Milliseconds(),
		})
	}

	// 17. Send response (AFTER metrics+logs, matches sibling handler ordering)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(core.ToolResponse{
		Success: true,
		Data:    result,
	})
}

// buildPlaywrightTestScript generates a Node.js script that uses playwright-extra
// with the stealth plugin to execute an ordered sequence of browser test actions.
// Each action maps 1:1 to a Playwright API call and produces a per-step result.
func buildPlaywrightTestScript(startURL string, actions []BrowserAction, timeoutSec, viewportWidth, viewportHeight int) string {
	overallTimeoutMs := timeoutSec * 1000

	// Escape strings for safe embedding in JS
	escapeJS := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `'`, `\'`)
		s = strings.ReplaceAll(s, "\n", `\n`)
		s = strings.ReplaceAll(s, "\r", `\r`)
		return s
	}

	// Build per-action try/catch blocks
	var actionBlocks strings.Builder
	for i, action := range actions {
		stepTimeout := action.Timeout
		if stepTimeout <= 0 {
			stepTimeout = 10000
		}

		selector := escapeJS(action.Selector)
		value := escapeJS(action.Value)
		expected := escapeJS(action.Expected)

		var jsCode string

		switch action.Action {
		case "click":
			jsCode = fmt.Sprintf(`await page.click('%s', { timeout: %d });`, selector, stepTimeout)
		case "fill":
			jsCode = fmt.Sprintf(`await page.fill('%s', '%s', { timeout: %d });`, selector, value, stepTimeout)
		case "select":
			jsCode = fmt.Sprintf(`await page.selectOption('%s', '%s', { timeout: %d });`, selector, value, stepTimeout)
		case "check":
			jsCode = fmt.Sprintf(`await page.check('%s', { timeout: %d });`, selector, stepTimeout)
		case "uncheck":
			jsCode = fmt.Sprintf(`await page.uncheck('%s', { timeout: %d });`, selector, stepTimeout)
		case "hover":
			jsCode = fmt.Sprintf(`await page.hover('%s', { timeout: %d });`, selector, stepTimeout)
		case "press":
			jsCode = fmt.Sprintf(`await page.press('%s', '%s', { timeout: %d });`, selector, value, stepTimeout)
		case "navigate":
			jsCode = fmt.Sprintf(`await page.goto('%s', { waitUntil: 'domcontentloaded', timeout: %d });`, value, stepTimeout)
		case "wait_for_selector":
			jsCode = fmt.Sprintf(`await page.waitForSelector('%s', { state: 'visible', timeout: %d });`, selector, stepTimeout)
		case "wait_for_url":
			jsCode = fmt.Sprintf(`await page.waitForURL('%s', { timeout: %d });`, value, stepTimeout)
		case "wait_for_network_idle":
			jsCode = `await page.waitForLoadState('networkidle');`
		case "screenshot":
			jsCode = fmt.Sprintf(`screenshots['%d'] = (await page.screenshot({ fullPage: true })).toString('base64');`, i)
		case "assert":
			jsCode = buildAssertionJS(action.Assertion, selector, expected, stepTimeout)
		default:
			// Unknown action — record as error
			actionBlocks.WriteString(fmt.Sprintf(`
  // Step %d: unknown action '%s'
  results.push({ step: %d, action: '%s', selector: '%s', passed: false, error: 'Unknown action type: %s', duration_ms: 0 });
`, i, escapeJS(action.Action), i, escapeJS(action.Action), selector, escapeJS(action.Action)))
			continue
		}

		// For assert actions, the JS sets a 'passed' variable; for all others, reaching the end = passed
		if action.Action == "assert" {
			actionBlocks.WriteString(fmt.Sprintf(`
  // Step %d: assert %s
  { const start_%d = Date.now();
    try {
      let passed = false;
      %s
      results.push({ step: %d, action: 'assert', selector: '%s', passed: passed, duration_ms: Date.now() - start_%d });
    } catch(e) {
      results.push({ step: %d, action: 'assert', selector: '%s', passed: false, error: e.message, duration_ms: Date.now() - start_%d });
    }
  }
`, i, escapeJS(action.Assertion), i, jsCode, i, selector, i, i, selector, i))
		} else {
			actionBlocks.WriteString(fmt.Sprintf(`
  // Step %d: %s
  { const start_%d = Date.now();
    try {
      %s
      results.push({ step: %d, action: '%s', selector: '%s', passed: true, duration_ms: Date.now() - start_%d });
    } catch(e) {
      results.push({ step: %d, action: '%s', selector: '%s', passed: false, error: e.message, duration_ms: Date.now() - start_%d });
    }
  }
`, i, escapeJS(action.Action), i, jsCode, i, escapeJS(action.Action), selector, i, i, escapeJS(action.Action), selector, i))
		}
	}

	script := fmt.Sprintf(`
const { chromium } = require('playwright-extra');
const stealth = require('puppeteer-extra-plugin-stealth');
chromium.use(stealth());

(async () => {
  let browser;
  try {
    browser = await chromium.launch({
      headless: true,
      args: ['--no-sandbox', '--disable-setuid-sandbox', '--disable-dev-shm-usage']
    });
    const context = await browser.newContext({ viewport: { width: %d, height: %d } });
    const page = await context.newPage();
    const results = [];
    const screenshots = {};
    const consoleLog = [];

    page.on('console', msg => consoleLog.push('[' + msg.type() + '] ' + msg.text()));

    await page.goto('%s', { waitUntil: 'domcontentloaded', timeout: %d });
%s
    const passed = results.every(r => r.passed);
    console.log(JSON.stringify({
      url: page.url(),
      passed: passed,
      total_steps: results.length,
      passed_steps: results.filter(r => r.passed).length,
      failed_steps: results.filter(r => !r.passed).length,
      steps: results,
      screenshots: screenshots,
      console_log: consoleLog
    }));
  } catch (err) {
    console.error(err.message);
    process.exit(1);
  } finally {
    if (browser) await browser.close();
  }
})();
`, viewportWidth, viewportHeight, escapeJS(startURL), overallTimeoutMs, actionBlocks.String())

	return script
}

// buildAssertionJS generates the JavaScript code for an assertion action.
// It sets a 'passed' boolean variable that the caller wraps in try/catch.
func buildAssertionJS(assertion, selector, expected string, timeoutMs int) string {
	switch assertion {
	case "visible":
		return fmt.Sprintf(`await page.locator('%s').waitFor({ state: 'visible', timeout: %d }); passed = true;`, selector, timeoutMs)
	case "hidden":
		return fmt.Sprintf(`await page.locator('%s').waitFor({ state: 'hidden', timeout: %d }); passed = true;`, selector, timeoutMs)
	case "text_contains":
		return fmt.Sprintf(`const txt = await page.locator('%s').textContent({ timeout: %d }); passed = txt !== null && txt.includes('%s');`, selector, timeoutMs, expected)
	case "text_equals":
		return fmt.Sprintf(`const txt = await page.locator('%s').textContent({ timeout: %d }); passed = txt !== null && txt.trim() === '%s';`, selector, timeoutMs, expected)
	case "url_contains":
		return fmt.Sprintf(`passed = page.url().includes('%s');`, expected)
	case "url_equals":
		return fmt.Sprintf(`passed = page.url() === '%s';`, expected)
	case "count_equals":
		return fmt.Sprintf(`passed = (await page.locator('%s').count()) === parseInt('%s');`, selector, expected)
	case "has_attribute":
		// Expected format: "attr=value"
		return fmt.Sprintf(`{
      const parts = '%s'.split('=');
      const attrName = parts[0];
      const attrVal = parts.slice(1).join('=');
      const actual = await page.locator('%s').getAttribute(attrName, { timeout: %d });
      passed = actual === attrVal;
    }`, expected, selector, timeoutMs)
	case "has_class":
		return fmt.Sprintf(`{
      const cls = await page.locator('%s').getAttribute('class', { timeout: %d });
      passed = cls !== null && cls.includes('%s');
    }`, selector, timeoutMs, expected)
	default:
		return fmt.Sprintf(`passed = false; /* unknown assertion type: %s */`, assertion)
	}
}

// --- ID Generation Helpers (stdlib only, no new dependencies) ---

// generateULID generates a ULID (Universally Unique Lexicographically Sortable Identifier).
// Format: 10 chars timestamp (ms since epoch, base32) + 16 chars randomness (base32)
func generateULID() string {
	const encoding = "0123456789ABCDEFGHJKMNPQRSTVWXYZ" // Crockford's base32

	now := time.Now().UnixMilli()

	// 10-character timestamp (48 bits, big-endian)
	ts := make([]byte, 10)
	for i := 9; i >= 0; i-- {
		ts[i] = encoding[now%32]
		now /= 32
	}

	// 16-character randomness (80 bits)
	randomBytes := make([]byte, 10)
	rand.Read(randomBytes)
	rnd := make([]byte, 16)
	for i := 0; i < 16; i++ {
		// Map random bytes to base32 encoding
		byteIndex := i * 5 / 8
		bitOffset := uint(i*5) % 8
		var val byte
		if bitOffset+5 <= 8 {
			val = (randomBytes[byteIndex] >> (3 - bitOffset)) & 0x1F
		} else if byteIndex+1 < len(randomBytes) {
			val = ((randomBytes[byteIndex] << (bitOffset - 3)) | (randomBytes[byteIndex+1] >> (11 - bitOffset))) & 0x1F
		} else {
			val = (randomBytes[byteIndex] << (bitOffset - 3)) & 0x1F
		}
		rnd[i] = encoding[val]
	}

	return string(ts) + string(rnd)
}

// generateNanoid generates a nanoid of the given length using crypto/rand.
func generateNanoid(size int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"
	result := make([]byte, size)
	for i := 0; i < size; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		result[i] = alphabet[n.Int64()]
	}
	return string(result)
}
