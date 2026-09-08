package redisprovider

import (
	"crypto/tls"
	"errors"
	"testing"

	"github.com/truvaagents/truva-g3/core"
)

func TestClientConfigAcceptsEveryExplicitTopology(t *testing.T) {
	tests := []struct {
		name    string
		options []ClientConfigOption
		mode    core.RedisMode
	}{
		{
			name: "standalone",
			options: []ClientConfigOption{
				WithConnectionMode(core.RedisModeStandalone),
				WithAddresses("standalone.example:6379"),
			},
			mode: core.RedisModeStandalone,
		},
		{
			name: "sentinel",
			options: []ClientConfigOption{
				WithConnectionMode(core.RedisModeSentinel),
				WithAddresses("sentinel-a.example:26379", "sentinel-b.example:26379"),
				WithSentinelMasterName("mymaster"),
			},
			mode: core.RedisModeSentinel,
		},
		{
			name: "cluster",
			options: []ClientConfigOption{
				WithConnectionMode(core.RedisModeCluster),
				WithAddresses("cluster.example:6379"),
				WithDatabase(0),
			},
			mode: core.RedisModeCluster,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, err := ConfigureClientConfig(DefaultClientConfig(), test.options...)
			if err != nil {
				t.Fatal(err)
			}
			if config.connection.Mode != test.mode {
				t.Fatalf("mode = %q, want %q", config.connection.Mode, test.mode)
			}
		})
	}
}

func TestClientConfigRejectsInvalidTopologyCombinations(t *testing.T) {
	tests := [][]ClientConfigOption{
		{
			WithConnectionMode(core.RedisModeStandalone),
			WithAddresses("standalone.example:6379"),
			WithDatabase(7),
		},
		{
			WithConnectionMode(core.RedisModeCluster),
			WithAddresses("cluster.example:6379"),
			WithDatabase(7),
		},
		{
			WithConnectionMode(core.RedisModeSentinel),
			WithAddresses("sentinel.example:26379"),
			WithSentinelMasterName(""),
		},
	}
	for _, options := range tests {
		if _, err := ConfigureClientConfig(DefaultClientConfig(), options...); err == nil {
			t.Fatal("invalid Redis topology was accepted")
		}
	}
}

func TestClientConfigRejectsNumberedDatabaseInCompleteConfig(t *testing.T) {
	tests := []core.RedisConnectionConfig{
		{Mode: core.RedisModeStandalone, Addrs: []string{"standalone.example:6379"}, DB: 7},
		{Mode: core.RedisModeSentinel, Addrs: []string{"sentinel.example:26379"}, MasterName: "mymaster", DB: 7},
	}
	for _, connection := range tests {
		_, err := ConfigureClientConfig(DefaultClientConfig(), WithConnectionConfig(connection))
		if !errors.Is(err, core.ErrInvalidConfiguration) {
			t.Fatalf("connection %#v error = %v, want ErrInvalidConfiguration", connection, err)
		}
	}
}

func TestClientConfigClonesTLSAndAddresses(t *testing.T) {
	tlsConfig := &tls.Config{ServerName: "redis.example"}
	addresses := []string{"cluster.example:6379"}
	config, err := ConfigureClientConfig(
		DefaultClientConfig(),
		WithConnectionMode(core.RedisModeCluster),
		WithAddresses(addresses...),
		WithTLSConfig(tlsConfig),
	)
	if err != nil {
		t.Fatal(err)
	}
	addresses[0] = "changed.example:6379"
	tlsConfig.ServerName = "changed.example"
	if config.connection.Addrs[0] != "cluster.example:6379" {
		t.Fatalf("addresses were not cloned: %#v", config.connection.Addrs)
	}
	if config.connection.TLSConfig.ServerName != "redis.example" {
		t.Fatalf("TLS config was not cloned: %#v", config.connection.TLSConfig)
	}
}

func TestClientConfigEnvironmentUsesSharedResolver(t *testing.T) {
	config, err := LoadClientConfigFromEnvironment(DefaultClientConfig(), lookupValues(map[string]string{
		"TRUVAG3_REDIS_URL": "redis://legacy.example:6379/0",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if config.connection.DB != 0 {
		t.Fatalf("canonical DB = %d, want 0", config.connection.DB)
	}
	if got := config.Diagnostics(); len(got) != 1 || got[0] != "TRUVAG3_REDIS_URL is deprecated; use REDIS_URL" {
		t.Fatalf("diagnostics = %#v", got)
	}

	_, err = LoadClientConfigFromEnvironment(DefaultClientConfig(), lookupValues(map[string]string{
		"TRUVAG3_REDIS_URL": "redis://legacy.example:6379/8",
	}))
	if err == nil {
		t.Fatal("canonical provider accepted a numbered database from the environment")
	}

	_, err = LoadClientConfigFromEnvironment(DefaultClientConfig(), lookupValues(map[string]string{
		"REDIS_URL":           "redis://standalone.example:6379",
		"TRUVAG3_REDIS_MODE":  "cluster",
		"TRUVAG3_REDIS_ADDRS": "cluster.example:6379",
	}))
	if err == nil {
		t.Fatal("mixed Redis connection sources were accepted")
	}
}

func TestPhaseTwoClusterProfileHasNoLegacyRoleDatabases(t *testing.T) {
	config, err := ConfigureClientConfig(
		DefaultClientConfig(),
		WithConnectionMode(core.RedisModeCluster),
		WithAddresses("cluster.example:6379"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.legacyRoleDB) != 0 {
		t.Fatalf("cluster profile retained legacy role databases: %#v", config.legacyRoleDB)
	}
}

func TestClusterEnvironmentIgnoresEmptyLegacyRoleDatabases(t *testing.T) {
	config, err := LoadClientConfigFromEnvironment(DefaultClientConfig(), lookupValues(map[string]string{
		"TRUVAG3_REDIS_MODE":               "cluster",
		"TRUVAG3_REDIS_ADDRS":              "cluster.example:6379",
		"TRUVAG3_REDIS_DB":                 "0",
		"TRUVAG3_EXECUTION_DEBUG_REDIS_DB": "",
		"TRUVAG3_LLM_DEBUG_REDIS_DB":       " ",
		"TRUVAG3_HITL_REDIS_DB":            "",
		"TRUVAG3_WORKFLOW_REDIS_DB":        "",
		"TRUVAG3_SCHEDULING_REDIS_DB":      "",
		"TRUVAG3_SKILLS_REDIS_DB":          "",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(config.legacyRoleDB) != 0 {
		t.Fatalf("cluster profile retained empty legacy role databases: %#v", config.legacyRoleDB)
	}
}
