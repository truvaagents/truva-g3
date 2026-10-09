package ai

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/truvaagents/truva-g3/core"
)

func TestRequestTimeoutOverrides(t *testing.T) {
	factory := withRecordingFactory(t)
	registry.providers[factory.Name()] = &timeoutRecordingRequestFactory{factory}
	constructors := map[string]func(time.Duration) error{
		"legacy": func(d time.Duration) error {
			_, err := NewClient(WithProvider(factory.Name()), WithTimeout(d))
			return err
		},
		"request": func(d time.Duration) error {
			_, err := NewRequestClient(WithProvider(factory.Name()), WithTimeout(d))
			return err
		},
		"chain": func(d time.Duration) error {
			_, err := NewChainClient(WithProviderChain(factory.Name()), WithChainTimeout(d))
			return err
		},
		"entry": func(d time.Duration) error {
			_, err := NewChain(ProviderEntry("entry", factory.Name(), WithTimeout(d)))
			return err
		},
	}
	for name, construct := range constructors {
		t.Run(name, func(t *testing.T) {
			t.Setenv(requestTimeoutEnv, "12m")
			for _, value := range []time.Duration{0, -time.Second, 9 * time.Minute} {
				require.NoError(t, construct(value))
				want := 12 * time.Minute
				if value > 0 {
					want = value
				}
				require.Equal(t, want, factory.lastConfig.Timeout)
			}
			for _, invalid := range []string{"", "0", "-1s", "300", "bad", "999999999999h"} {
				t.Setenv(requestTimeoutEnv, invalid)
				require.ErrorIs(t, construct(time.Minute), core.ErrInvalidConfiguration)
			}
			require.NoError(t, os.Unsetenv(requestTimeoutEnv))
			require.NoError(t, construct(0))
			require.Equal(t, defaultRequestTimeout, factory.lastConfig.Timeout)
		})
	}
}

type timeoutRecordingRequestFactory struct{ *recordingFactory }

func (f *timeoutRecordingRequestFactory) CreateRequestClient(config *AIConfig, _ ProviderIntegrationConfig) (core.AIRequestClient, error) {
	f.lastConfig = config
	return &phase5RequestClient{}, nil
}

func TestRequestTimeoutEnvironmentPrecedesProviderDefault(t *testing.T) {
	original := registry
	t.Cleanup(func() { registry = original })
	factory := &timeoutDefaultFactory{mockFactory: &mockFactory{name: "long-timeout", client: &mockAIClient{}}, defaultTimeout: time.Hour}
	registry = &ProviderRegistry{providers: map[string]ProviderFactory{factory.Name(): factory}}
	t.Setenv(requestTimeoutEnv, "12m")
	_, err := NewClient(WithProvider(factory.Name()))
	require.NoError(t, err)
	require.Equal(t, 12*time.Minute, factory.lastConfig.Timeout)
}

func TestInjectedClientTimeoutRemainsCallerOwned(t *testing.T) {
	t.Setenv(requestTimeoutEnv, "invalid")
	_, err := NewChain(ClientEntry("custom", &phase5RequestClient{}))
	require.NoError(t, err)
}
