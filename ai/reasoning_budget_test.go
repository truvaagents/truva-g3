package ai

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func TestReasoningMultiplierResolution(t *testing.T) {
	t.Setenv(reasoningMultiplierEnv, "")
	require.NoError(t, os.Unsetenv(reasoningMultiplierEnv))
	got, err := ResolveReasoningTokenMultiplier(0)
	require.NoError(t, err)
	require.Equal(t, 3, got)
	t.Setenv(reasoningMultiplierEnv, "5")
	got, err = ResolveReasoningTokenMultiplier(0)
	require.NoError(t, err)
	require.Equal(t, 5, got)
	got, err = ResolveReasoningTokenMultiplier(1)
	require.NoError(t, err)
	require.Equal(t, 1, got)
	_, err = ResolveReasoningTokenMultiplier(-1)
	require.ErrorIs(t, err, core.ErrInvalidConfiguration)
	for _, raw := range []string{"", "0", "-2", "1.5", "sensitive-invalid", "99999999999999999999999999999"} {
		t.Setenv(reasoningMultiplierEnv, raw)
		for _, code := range []int{0, 1} {
			_, err := ResolveReasoningTokenMultiplier(code)
			require.ErrorIs(t, err, core.ErrInvalidConfiguration)
			if raw == "sensitive-invalid" {
				require.NotContains(t, err.Error(), raw)
			}
		}
		_, err := NewClient()
		require.ErrorIs(t, err, core.ErrInvalidConfiguration)
		_, err = NewChainClient()
		require.ErrorIs(t, err, core.ErrInvalidConfiguration)
	}
}
