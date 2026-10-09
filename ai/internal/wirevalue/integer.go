// Package wirevalue reads numeric wire values without rewriting caller-owned data.
package wirevalue

import (
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// Int accepts numeric values that exactly fit a Go int. JSON decimal/exponent
// notation is checked without a float64 round trip. Strings are not numbers.
func Int(value interface{}) (int, bool) {
	if number, ok := value.(json.Number); ok {
		return jsonInt(string(number))
	}
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return 0, false
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := v.Int()
		if n >= math.MinInt && n <= math.MaxInt {
			return int(n), true
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n := v.Uint()
		if n <= math.MaxInt {
			return int(n), true
		}
	case reflect.Float32, reflect.Float64:
		n := v.Float()
		// The exclusive upper bound avoids rounding MaxInt up to 2^63.
		upper := math.Ldexp(1, strconv.IntSize-1)
		if n >= -upper && n < upper && math.Trunc(n) == n {
			return int(n), true
		}
	}
	return 0, false
}

func jsonInt(raw string) (int, bool) {
	if raw == "" || strings.TrimSpace(raw) != raw || !json.Valid([]byte(raw)) ||
		(raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
		return 0, false
	}
	negative := strings.HasPrefix(raw, "-")
	mantissa := strings.TrimPrefix(raw, "-")
	exponentText := "0"
	if index := strings.IndexAny(mantissa, "eE"); index >= 0 {
		exponentText = mantissa[index+1:]
		mantissa = mantissa[:index]
	}
	fractionDigits := 0
	if index := strings.IndexByte(mantissa, '.'); index >= 0 {
		fractionDigits = len(mantissa) - index - 1
		mantissa = mantissa[:index] + mantissa[index+1:]
	}
	digits := strings.TrimRight(mantissa, "0")
	scale := fractionDigits - (len(mantissa) - len(digits))
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return 0, true
	}
	// Bound before expanding exponents, including hostile numeric literals.
	maxDigits := len(strconv.Itoa(math.MaxInt))
	if len(digits) > maxDigits {
		return 0, false
	}
	exponent, err := strconv.ParseInt(exponentText, 10, 64)
	if err != nil || exponent < int64(scale) || exponent > int64(scale)+int64(maxDigits-len(digits)) {
		return 0, false
	}
	integer := digits + strings.Repeat("0", int(exponent-int64(scale)))
	if negative {
		integer = "-" + integer
	}
	n, err := strconv.Atoi(integer)
	return n, err == nil
}
