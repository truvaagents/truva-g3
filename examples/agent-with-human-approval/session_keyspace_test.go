package main

import (
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/truvaagents/truva-g3/core"
)

func TestSessionRuntimeUsesDBZeroAndDeploymentScopedKeys(t *testing.T) {
	server := miniredis.RunT(t)
	connection := core.DefaultRedisConnectionConfig()
	connection.Addrs = []string{server.Addr()}
	now := time.Now()
	type deploymentFixture struct {
		name     string
		title    string
		keyspace core.RedisKeyspace
		store    *SessionStore
	}
	deployments := []deploymentFixture{
		{name: "deployment-a", title: "title from deployment A"},
		{name: "deployment-b", title: "title from deployment B"},
	}
	parentT := t
	for index := range deployments {
		deployment := &deployments[index]
		t.Run("write/"+deployment.name, func(t *testing.T) {
			keyspace, err := core.NewRedisKeyspace(deployment.name)
			if err != nil {
				t.Fatal(err)
			}
			store, err := NewSessionStore(connection, keyspace, time.Minute, 10, &core.NoOpLogger{})
			if err != nil {
				t.Fatal(err)
			}
			deployment.keyspace, deployment.store = keyspace, store
			parentT.Cleanup(func() { _ = store.Close() })
			if err := store.saveSession(t.Context(), &Session{
				ID: "shared-session", UserID: "user-1", Title: deployment.title,
				CreatedAt: now, UpdatedAt: now, Messages: []Message{},
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
	for index := range deployments {
		deployment := &deployments[index]
		t.Run("read/"+deployment.name, func(t *testing.T) {
			loaded := deployment.store.Get("shared-session")
			if loaded == nil || loaded.Title != deployment.title {
				t.Fatalf("loaded session = %#v, want title %q", loaded, deployment.title)
			}
			key := deployment.keyspace.Plain("sessions") + ":shared-session"
			if !server.DB(0).Exists(key) || server.DB(2).Exists(key) {
				t.Fatalf("session key %q was not isolated to DB 0", key)
			}
		})
	}
	numbered := connection
	numbered.DB = 2
	_, err := NewSessionStore(numbered, deployments[0].keyspace, time.Minute, 10, &core.NoOpLogger{})
	if !errors.Is(err, core.ErrInvalidConfiguration) {
		t.Fatalf("numbered DB constructor error = %v, want ErrInvalidConfiguration", err)
	}
}
