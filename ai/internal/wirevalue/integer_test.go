package wirevalue

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntExactNumericValues(t *testing.T) {
	type namedInt int
	for _, value := range []interface{}{int(1000), int16(1000), int32(1000), int64(1000), uint(1000), uint16(1000), uint32(1000), uint64(1000), float32(1000), float64(1000), namedInt(1000), json.Number("1000"), json.Number("1e3"), json.Number("1000.0"), json.Number("0.001E+6"), json.Number("100000e-2")} {
		n, ok := Int(value)
		require.True(t, ok, "%T(%v)", value, value)
		require.Equal(t, 1000, n)
	}
	for _, n := range []int{math.MinInt, math.MaxInt, -1000, 0} {
		for _, value := range []interface{}{n, json.Number(strconv.Itoa(n)), json.Number(strconv.Itoa(n) + ".0")} {
			got, ok := Int(value)
			require.True(t, ok, "%v", value)
			require.Equal(t, n, got)
		}
	}
	for _, raw := range []string{"0e99999999999999999999999", "-0.00e-99999999999999999999999"} {
		n, ok := Int(json.Number(raw))
		require.True(t, ok)
		require.Zero(t, n)
	}
	for _, value := range []interface{}{nil, "1000", true, []int{1}, 1.5, math.NaN(), math.Inf(1), math.Inf(-1), uint64(math.MaxUint64), math.Ldexp(1, strconv.IntSize-1), -math.Ldexp(1, strconv.IntSize), json.Number("1000.00000000000000000001"), json.Number("1e100000000000000000000"), json.Number("1e-100000000000000000000"), json.Number("1e20"), json.Number("1e-1"), json.Number("9223372036854775808"), json.Number("-9223372036854775809"), json.Number("123456789012345678901"), json.Number(""), json.Number(" 1 "), json.Number("01"), json.Number("true"), json.Number("\"1\""), json.Number("NaN")} {
		_, ok := Int(value)
		require.False(t, ok, "%T(%v)", value, value)
	}
}

func FuzzJSONInt(f *testing.F) {
	for _, raw := range []string{"1e3", "1000.0", "1.01", "-123", "9223372036854775807", "1000.00000000000000000001", "0e99999999999999999"} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		// Bound only the independent big.Rat oracle's allocations.
		if len(raw) > 128 {
			t.Skip()
		}
		if index := strings.IndexAny(raw, "eE"); index >= 0 {
			exponent, err := strconv.Atoi(raw[index+1:])
			if err != nil || exponent < -1000 || exponent > 1000 {
				t.Skip()
			}
		}
		got, ok := Int(json.Number(raw))
		valid := raw != "" && strings.TrimSpace(raw) == raw && json.Valid([]byte(raw)) && (raw[0] == '-' || raw[0] >= '0' && raw[0] <= '9')
		if !valid {
			require.False(t, ok)
			return
		}
		exact, parsed := new(big.Rat).SetString(raw)
		require.True(t, parsed)
		wantOK := exact.IsInt() && exact.Num().IsInt64() && exact.Num().Int64() >= math.MinInt && exact.Num().Int64() <= math.MaxInt
		require.Equal(t, wantOK, ok)
		if wantOK {
			require.Equal(t, int(exact.Num().Int64()), got)
		}
	})
}
