package anthropic

import (
	"errors"
	"fmt"

	"github.com/truvaagents/truva-g3/ai/requestpolicy"
)

type anthropicDraft struct {
	*requestpolicy.Document
	explicit map[string]struct{}
	profile  requestProfile
	stream   bool
}

func (d *anthropicDraft) HasExplicitIntent(path string) bool {
	_, ok := d.explicit[path]
	return ok
}

func (d *anthropicDraft) PolicyFingerprintIdentity() string {
	return d.profile.fingerprintIdentity
}

func (d *anthropicDraft) Validate() error {
	if err := d.profile.validate(); err != nil {
		return err
	}
	switch d.profile.modelField {
	case modelInBody:
		model, ok := d.Get("/model")
		if !ok || model != d.profile.wireModel {
			return errors.New("anthropic body model invariant was not preserved")
		}
	case modelInRoute:
		if _, ok := d.Get("/model"); ok {
			return errors.New("vertex Anthropic body model must be omitted")
		}
	default:
		return errors.New("anthropic model-field mode is invalid")
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
	version, bodyVersion := d.Get("/anthropic_version")
	switch d.profile.versionPlacement {
	case versionInBody:
		if !bodyVersion || version != d.profile.version {
			return errors.New("vertex Anthropic body version invariant was not preserved")
		}
	case versionInHeader:
		if bodyVersion {
			return errors.New("direct Anthropic version must not appear in the body")
		}
	default:
		return errors.New("anthropic version placement is invalid")
	}
	return nil
}
