package entry

import (
	"context"
	"errors"
	"net/http"
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
// The message is actionable, not generic: a rejected key says to fix the key,
// a wrong endpoint says to fix base_url/model, rate limits and outages say to
// wait — so the user knows what to do instead of retrying blind.
func WrapAPIError(err error) error {
	switch {
	case errors.Is(err, ErrNoKey):
		return &ClassifyError{
			UserMsg: "未配置模型 API key（config model.api_key 或 OPENPANDA_MODEL_API_KEY）",
			Err:     err,
		}
	case errors.Is(err, context.Canceled):
		return &ClassifyError{UserMsg: "模型调用被取消", Err: err}
	case errors.Is(err, context.DeadlineExceeded):
		return &ClassifyError{UserMsg: "模型调用超时，请检查网络后重试", Err: err}
	}

	// A definitive provider rejection (the request was answered, not lost).
	var se *statusError
	if errors.As(err, &se) {
		switch {
		case se.status == http.StatusUnauthorized || se.status == http.StatusForbidden:
			return &ClassifyError{UserMsg: "模型 API key 无效或无权限，请在设置中检查 model.api_key", Err: err}
		case se.status == http.StatusNotFound:
			return &ClassifyError{UserMsg: "模型接口不存在，请检查 base_url 与 model 名称是否正确", Err: err}
		case se.status == http.StatusBadRequest:
			return &ClassifyError{UserMsg: "模型服务拒绝了请求（400），请检查 model 名称与 api_type 是否匹配", Err: err}
		}
	}

	// A retryable failure that survived its retries: the provider was
	// rate-limiting or down the whole time.
	var re *retryableError
	if errors.As(err, &re) {
		if re.status == http.StatusTooManyRequests {
			return &ClassifyError{UserMsg: "模型服务限流（已自动重试仍失败），请稍后再试", Err: err}
		}
		return &ClassifyError{UserMsg: "模型服务暂时不可用（已自动重试仍失败），请稍后再试", Err: err}
	}

	// A transport-level failure that survived its retries: the endpoint was
	// never reached.
	var te *transientError
	if errors.As(err, &te) {
		return &ClassifyError{UserMsg: "无法连接模型服务，请检查网络与 base_url 是否可达", Err: err}
	}

	return &ClassifyError{UserMsg: "入口模型调用失败，请稍后重试", Err: err}
}
