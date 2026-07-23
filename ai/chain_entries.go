package ai

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/truvaagents/truva-g3/core"
)

type chainEntryKind uint8

const (
	chainRequestProvider chainEntryKind = iota
	chainLegacyProvider
	chainInjectedClient
)

// ChainEntry is one independently configured participant in a provider
// failover chain. Construct entries with ProviderEntry or ClientEntry.
type ChainEntry struct {
	name          string
	providerAlias string
	options       []ClientOption
	legacyOptions []AIOption
	client        core.AIClient
	kind          chainEntryKind
}

// ProviderEntry constructs a framework-managed, request-capable provider
// entry. The provider alias is applied after all supplied options so an entry
// cannot accidentally materialize a different provider. The selected factory
// must support request-aware construction. Built-in support currently includes
// Anthropic, Azure OpenAI, OpenAI, and Bedrock when the bedrock build tag is
// enabled.
func ProviderEntry(name, providerAlias string, options ...ClientOption) ChainEntry {
	return ChainEntry{
		name:          name,
		providerAlias: providerAlias,
		options:       append([]ClientOption(nil), options...),
		kind:          chainRequestProvider,
	}
}

// ClientEntry constructs a caller-owned client entry. The chain invokes the
// client but never mutates it through optional logger, telemetry, or lifecycle
// setters.
func ClientEntry(name string, client core.AIClient) ChainEntry {
	return ChainEntry{name: name, client: client, kind: chainInjectedClient}
}

func legacyProviderEntry(name, providerAlias string, options ...AIOption) ChainEntry {
	return ChainEntry{
		name:          name,
		providerAlias: providerAlias,
		legacyOptions: append([]AIOption(nil), options...),
		kind:          chainLegacyProvider,
	}
}

// NewChain constructs a heterogeneous request-aware failover chain. Entry
// names must be unique, stable non-secret operator labels because they are
// emitted in sanitized reports, logs, and traces.
func NewChain(entries ...ChainEntry) (*ChainClient, error) {
	validated, err := validateAndCopyEntries(entries)
	if err != nil {
		return nil, err
	}
	materialized, err := materializeEntries(validated)
	if err != nil {
		return nil, err
	}
	return newChainFromEntries(materialized, &core.NoOpLogger{}, nil), nil
}

func validateAndCopyEntries(entries []ChainEntry) ([]ChainEntry, error) {
	if len(entries) == 0 {
		return nil, errors.New("AI chain requires at least one entry")
	}

	validated := make([]ChainEntry, len(entries))
	names := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		if err := validateChainEntryLabel("name", entry.name); err != nil {
			return nil, fmt.Errorf("AI chain entry %d: %w", index, err)
		}
		if _, duplicate := names[entry.name]; duplicate {
			return nil, fmt.Errorf("AI chain entry %d has duplicate name %q", index, entry.name)
		}
		names[entry.name] = struct{}{}

		clone := entry
		switch entry.kind {
		case chainRequestProvider:
			if err := validateChainEntryLabel("provider alias", entry.providerAlias); err != nil {
				return nil, fmt.Errorf("AI chain entry %q: %w", entry.name, err)
			}
			clone.options = append([]ClientOption(nil), entry.options...)
			clone.legacyOptions = nil
			clone.client = nil
		case chainLegacyProvider:
			if err := validateChainEntryLabel("provider alias", entry.providerAlias); err != nil {
				return nil, fmt.Errorf("AI chain entry %q: %w", entry.name, err)
			}
			clone.legacyOptions = append([]AIOption(nil), entry.legacyOptions...)
			clone.options = nil
			clone.client = nil
		case chainInjectedClient:
			if isNilChainClient(entry.client) {
				return nil, fmt.Errorf("AI chain entry %q has a nil client", entry.name)
			}
			clone.options = nil
			clone.legacyOptions = nil
		default:
			return nil, fmt.Errorf("AI chain entry %q has invalid kind %d", entry.name, entry.kind)
		}
		validated[index] = clone
	}
	return validated, nil
}

func validateChainEntryLabel(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", field)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s %q has surrounding whitespace", field, value)
	}
	if len(value) > 256 {
		return fmt.Errorf("%s exceeds 256 bytes", field)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s contains control characters", field)
		}
	}
	return nil
}

func isNilChainClient(client core.AIClient) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func materializeEntries(entries []ChainEntry) ([]ChainEntry, error) {
	materialized := make([]ChainEntry, 0, len(entries))
	for index, entry := range entries {
		resolved, err := materializeEntry(entry)
		if err != nil {
			return nil, fmt.Errorf("materialize AI chain entry %d (%q): %w", index, entry.name, err)
		}
		materialized = append(materialized, resolved)
	}
	return materialized, nil
}

func materializeEntry(entry ChainEntry) (ChainEntry, error) {
	resolved := entry
	switch entry.kind {
	case chainRequestProvider:
		options := make([]ClientOption, 0, len(entry.options)+2)
		options = append(options, entry.options...)
		options = append(options, withDefaultRequestTimeout(defaultRequestTimeout))
		options = append(options, WithProviderAlias(entry.providerAlias))
		client, err := NewRequestClient(options...)
		if err != nil {
			return ChainEntry{}, err
		}
		resolved.options = nil
		resolved.client = client
	case chainLegacyProvider:
		options := make([]AIOption, 0, len(entry.legacyOptions)+2)
		options = append(options, entry.legacyOptions...)
		options = append(options, withDefaultRequestTimeout(defaultRequestTimeout))
		options = append(options, WithProviderAlias(entry.providerAlias))
		client, err := NewClient(options...)
		if err != nil {
			return ChainEntry{}, err
		}
		resolved.legacyOptions = nil
		resolved.client = client
	case chainInjectedClient:
		// Caller-owned clients are already materialized.
	default:
		return ChainEntry{}, fmt.Errorf("invalid chain entry kind %d", entry.kind)
	}
	return resolved, nil
}

func withDefaultRequestTimeout(timeout time.Duration) AIOption {
	return func(config *AIConfig) {
		if config.Timeout <= 0 {
			config.Timeout = timeout
		}
	}
}

func newChainFromEntries(entries []ChainEntry, logger core.Logger, telemetry core.Telemetry) *ChainClient {
	if logger == nil {
		logger = &core.NoOpLogger{}
	}
	client := &ChainClient{
		entries:         append([]ChainEntry(nil), entries...),
		providers:       make([]core.AIClient, 0, len(entries)),
		providerAliases: make([]string, 0, len(entries)),
		logger:          logger,
		telemetry:       telemetry,
	}
	for _, entry := range entries {
		client.providers = append(client.providers, entry.client)
		client.providerAliases = append(client.providerAliases, entry.name)
	}
	return client
}

func (c *ChainClient) runtimeEntries() []ChainEntry {
	if len(c.entries) > 0 {
		return c.entries
	}
	entries := make([]ChainEntry, 0, len(c.providers))
	for index, client := range c.providers {
		name := fmt.Sprintf("provider-%d", index+1)
		if index < len(c.providerAliases) && c.providerAliases[index] != "" {
			name = c.providerAliases[index]
		}
		entries = append(entries, ChainEntry{
			name:   name,
			client: client,
			kind:   chainLegacyProvider,
		})
	}
	return entries
}
