module github.com/truvaagents/truva-g3

go 1.26.4

require github.com/truvaagents/truva-g3/core v0.2.0

replace github.com/truvaagents/truva-g3/core => ./core

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/go-redis/redis/v8 v8.11.5 // indirect
	github.com/google/uuid v1.6.0 // indirect
	golang.org/x/text v0.37.0 // indirect
)

// v0.1.0 was published from a workspace with relative replace directives and an
// unresolvable core requirement, so external consumers cannot build it. It is
// superseded by v0.2.0.
retract v0.1.0
