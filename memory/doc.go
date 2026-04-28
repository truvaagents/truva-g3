// Package memory provides storage backend implementations for Truva-G3's
// cross-agent shared memory interfaces defined in core.
//
// This package exists to isolate heavy client dependencies (gRPC, protobuf)
// from the core module. Applications that don't use shared knowledge
// (Phase 2) never need to import this package.
//
// Default vector search backend: Qdrant (Apache 2.0, gRPC) via vendor-agnostic VectorSharedKnowledge.
//
// Usage:
//
//	knowledge, err := memory.NewVectorSharedKnowledge(
//	    memory.WithVectorAddress("qdrant:6334"),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer knowledge.Close()
//
// See ARCHITECTURE.md for design decisions and integration patterns.
package memory
