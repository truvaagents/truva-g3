module github.com/truvaagents/truva-g3

go 1.27.0

require github.com/truvaagents/truva-g3/core v0.4.0

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/redis/go-redis/v9 v9.22.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

// v0.1.0 was published from a workspace with relative replace directives and an
// unresolvable core requirement, so external consumers cannot build it. It is
// superseded by v0.2.0.
retract v0.1.0
