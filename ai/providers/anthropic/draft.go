package anthropic

import (
	"errors"
	"fmt"

	"github.com/truvaagents/truva-g3/ai/requestpolicy"
)

const anthropicMessagesAdapterVersion = "anthropic-messages-v1"

type anthropicDraft struct {
	*requestpolicy.Document
	explicit map[string]struct{}
	stream   bool
}

func (d *anthropicDraft) HasExplicitIntent(path string) bool {
	_, ok := d.explicit[path]
	return ok
}

func (d *anthropicDraft) PolicyFingerprintIdentity() string {
	return anthropicMessagesAdapterVersion
}

func (d *anthropicDraft) Validate() error {
	model, ok := d.Get("/model")
	if !ok || model != d.Info().ResolvedModel {
		return errors.New("resolved model invariant was not preserved")
	}
	if _, ok := d.Get("/messages"); !ok {
		return errors.New("messages input is required")
	}
	maxTokens, ok := d.Get("/max_tokens")
	if !ok {
		return errors.New("max_tokens is required by the Anthropic Messages API")
	}
	switch value := maxTokens.(type) {
	case int:
		if value <= 0 {
			return errors.New("max_tokens must be positive")
		}
	case int32:
		if value <= 0 {
			return errors.New("max_tokens must be positive")
		}
	case int64:
		if value <= 0 {
			return errors.New("max_tokens must be positive")
		}
	default:
		return fmt.Errorf("max_tokens has unsupported type %T", maxTokens)
	}
	stream, exists := d.Get("/stream")
	if d.stream {
		if !exists || stream != true {
			return errors.New("streaming invariant was not preserved")
		}
	} else if exists {
		return errors.New("non-streaming request cannot enable streaming")
	}
	return nil
}
