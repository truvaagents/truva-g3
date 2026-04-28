package orchestration

import (
	"testing"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/core/conformance"
)

func TestInMemoryTaskConsumerConformance(t *testing.T) {
	conformance.RunTaskConsumerConformance(t, func(t *testing.T) (core.TaskConsumer, core.TaskDispatcher, func()) {
		dispatcher := NewInMemoryTaskDispatcher()
		consumer := NewInMemoryTaskConsumerFromDispatcher(dispatcher)
		return consumer, dispatcher, func() {}
	})
}
