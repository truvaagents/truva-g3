package redistest

import "testing"

func TestPortableClusterDistributions(t *testing.T) {
	for _, distribution := range []Distribution{RedisOSS, Valkey8, Valkey9} {
		if !distribution.Valid() {
			t.Fatalf("required distribution %q is invalid", distribution)
		}
	}
	if Distribution("unknown").Valid() {
		t.Fatal("unknown distribution was accepted")
	}
}
