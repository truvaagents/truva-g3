package openai

import (
	"errors"
	"fmt"

	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/core"
)

type compatibleProviderError struct {
	statusCode int
	provider   string
	model      string
	retryable  bool
}

func (e *compatibleProviderError) Error() string {
	return fmt.Sprintf("OpenAI-compatible request failed (status %d)", e.statusCode)
}

func (e *compatibleProviderError) StatusCode() int   { return e.statusCode }
func (e *compatibleProviderError) Provider() string  { return e.provider }
func (e *compatibleProviderError) Model() string     { return e.model }
func (*compatibleProviderError) IsTransient() bool   { return false }
func (e *compatibleProviderError) IsRetryable() bool { return e.retryable }

func (c *Client) normalizeCompatibleError(err error, model string) error {
	var endpointErr *openaiwire.EndpointError
	if !errors.As(err, &endpointErr) {
		return err
	}
	if c.getProviderName() == openRouterProviderAlias {
		return normalizeOpenRouterError(0, endpointErr, model)
	}
	status := endpointErr.Code
	if !validProviderErrorStatus(status) {
		status = 500
	}
	return &compatibleProviderError{
		statusCode: status,
		provider:   c.getProviderName(),
		model:      model,
		retryable:  status == 402,
	}
}

func validProviderErrorStatus(status int) bool { return status >= 400 && status <= 599 }

var _ core.ProviderError = (*compatibleProviderError)(nil)
