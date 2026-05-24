module github.com/truvaagents/truva-g3

go 1.26.3

require github.com/truvaagents/truva-g3/core v0.0.0-00010101000000-000000000000

replace github.com/truvaagents/truva-g3/core => ./core

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/go-redis/redis/v8 v8.11.5 // indirect
	github.com/google/uuid v1.6.0 // indirect
	golang.org/x/net v0.52.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
)
