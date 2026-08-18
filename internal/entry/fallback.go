package entry

import (
	"context"
	"errors"
)

// ClassifyError wraps a model/parse failure with a user-friendly message. The
// entry layer must never fail silently — an API error surfaces a clear
// explanation, and a malformed structured output degrades to an answer rather
// than erroring out (see ParseOutput).
type ClassifyError struct {
	UserMsg string // what to show the user
	Err     error  // underlying cause
}

func (e *ClassifyError) Error() string { return e.UserMsg }
func (e *ClassifyError) Unwrap() error { return e.Err }

// WrapAPIError converts a model-call error into a user-friendly ClassifyError.
func WrapAPIError(err error) error {
	switch {
	case errors.Is(err, ErrNoKey):
		return &ClassifyError{
			UserMsg: "未配置模型 API key（config model.api_key 或 OPENPANDA_MODEL_API_KEY）",
			Err:     err,
		}
	case errors.Is(err, context.Canceled):
		return &ClassifyError{UserMsg: "模型调用被取消", Err: err}
	default:
		return &ClassifyError{UserMsg: "入口模型调用失败，请稍后重试", Err: err}
	}
}
