//go:build bedrock
// +build bedrock

package bedrock

import (
	"context"
	"errors"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/truvaagents/truva-g3/core"
)

type bedrockProviderError struct {
	cause      error
	code       string
	statusCode int
	model      string
	transient  bool
	retryable  bool
}

func (e *bedrockProviderError) Error() string {
	if e == nil || e.cause == nil {
		return "Bedrock provider request failed"
	}
	return e.cause.Error()
}

func (e *bedrockProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *bedrockProviderError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.statusCode
}
func (*bedrockProviderError) Provider() string { return "bedrock" }
func (e *bedrockProviderError) Model() string {
	if e == nil {
		return ""
	}
	return e.model
}
func (e *bedrockProviderError) IsTransient() bool { return e != nil && e.transient }
func (e *bedrockProviderError) IsRetryable() bool { return e != nil && e.retryable }

type awsHTTPStatusError interface{ HTTPStatusCode() int }
type awsCodedError interface{ ErrorCode() string }

func normalizeBedrockError(err error, semanticModel string) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var existing core.ProviderError
	if errors.As(err, &existing) {
		return err
	}

	classification, ok := classifyBedrockError(err)
	if !ok {
		return err
	}
	var responseError awsHTTPStatusError
	if errors.As(err, &responseError) && responseError.HTTPStatusCode() > 0 {
		classification.statusCode = responseError.HTTPStatusCode()
	}
	var coded awsCodedError
	if errors.As(err, &coded) && coded.ErrorCode() != "" {
		classification.code = coded.ErrorCode()
	}
	classification.cause = err
	classification.model = semanticModel
	return classification
}

func classifyBedrockError(err error) (*bedrockProviderError, bool) {
	var accessDenied *types.AccessDeniedException
	if errors.As(err, &accessDenied) {
		return newBedrockError("AccessDeniedException", http.StatusForbidden, false, false), true
	}
	var conflict *types.ConflictException
	if errors.As(err, &conflict) {
		return newBedrockError("ConflictException", http.StatusConflict, false, false), true
	}
	var internal *types.InternalServerException
	if errors.As(err, &internal) {
		return newBedrockError("InternalServerException", http.StatusInternalServerError, false, false), true
	}
	var modelError *types.ModelErrorException
	if errors.As(err, &modelError) {
		status := http.StatusFailedDependency
		if modelError.OriginalStatusCode != nil && *modelError.OriginalStatusCode > 0 {
			status = int(*modelError.OriginalStatusCode)
		}
		return newBedrockError("ModelErrorException", status, false, true), true
	}
	var modelNotReady *types.ModelNotReadyException
	if errors.As(err, &modelNotReady) {
		return newBedrockError("ModelNotReadyException", http.StatusTooManyRequests, false, true), true
	}
	var modelStream *types.ModelStreamErrorException
	if errors.As(err, &modelStream) {
		status := http.StatusFailedDependency
		if modelStream.OriginalStatusCode != nil && *modelStream.OriginalStatusCode > 0 {
			status = int(*modelStream.OriginalStatusCode)
		}
		return newBedrockError("ModelStreamErrorException", status, false, true), true
	}
	var modelTimeout *types.ModelTimeoutException
	if errors.As(err, &modelTimeout) {
		return newBedrockError("ModelTimeoutException", http.StatusRequestTimeout, false, true), true
	}
	var resourceNotFound *types.ResourceNotFoundException
	if errors.As(err, &resourceNotFound) {
		return newBedrockError("ResourceNotFoundException", http.StatusNotFound, false, false), true
	}
	var quota *types.ServiceQuotaExceededException
	if errors.As(err, &quota) {
		return newBedrockError("ServiceQuotaExceededException", http.StatusBadRequest, false, true), true
	}
	var unavailable *types.ServiceUnavailableException
	if errors.As(err, &unavailable) {
		return newBedrockError("ServiceUnavailableException", http.StatusServiceUnavailable, false, false), true
	}
	var throttling *types.ThrottlingException
	if errors.As(err, &throttling) {
		return newBedrockError("ThrottlingException", http.StatusTooManyRequests, false, false), true
	}
	var validation *types.ValidationException
	if errors.As(err, &validation) {
		return newBedrockError("ValidationException", http.StatusBadRequest, false, false), true
	}
	return nil, false
}

func newBedrockError(code string, status int, transient, retryable bool) *bedrockProviderError {
	return &bedrockProviderError{
		code: code, statusCode: status, transient: transient, retryable: retryable,
	}
}

var _ core.ProviderError = (*bedrockProviderError)(nil)
