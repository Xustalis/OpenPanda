package entry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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
			UserMsg: "未配置模型 API key：运行 `panda init` 完成引导配置，或打开 `panda web` 在设置页填写（也可用 OPENPANDA_MODEL_API_KEY 环境变量）",
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
			// The stock hint names the two most common causes, but a 400 can be
			// anything the provider dislikes (a retired model name, an oversized
			// context) — the provider's own reason rides along so the user fixes
			// the real cause instead of chasing the hint.
			msg := "模型服务拒绝了请求（400），请检查 model 名称与 api_type 是否匹配"
			if detail := providerDetail(se.body); detail != "" {
				msg += "：" + detail
			}
			return &ClassifyError{UserMsg: msg, Err: err}
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

// providerDetail extracts the provider's rejection reason from a response
// body for display: the message field of the usual {"error":{"message":…}}
// envelope when the body is JSON, the first line otherwise. Bounded, so a
// verbose provider cannot flood the chat bubble.
func providerDetail(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var envelope struct {
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &envelope) == nil {
		if envelope.Error.Message != "" {
			return truncate(envelope.Error.Message, 200)
		}
		if envelope.Message != "" {
			return truncate(envelope.Message, 200)
		}
	}
	line := body
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return truncate(strings.TrimSpace(line), 200)
}

// IsFatalModelError reports whether an error from the model client indicates
// that the provider or endpoint failed in an unrecoverable manner (unauthorized 401,
// forbidden/quota 403, not found 404, rate limited 429 after retries, 5xx server outage,
// or connection unreachable), making fallback to an alternate model candidate desirable.
// User context cancellation (context.Canceled) is NOT considered a fatal model error.
func IsFatalModelError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, ErrNoKey) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var se *statusError
	if errors.As(err, &se) {
		return se.status == http.StatusUnauthorized ||
			se.status == http.StatusForbidden ||
			se.status == http.StatusNotFound ||
			se.status == http.StatusTooManyRequests ||
			se.status >= 500
	}
	var re *retryableError
	if errors.As(err, &re) {
		return true
	}
	var te *transientError
	if errors.As(err, &te) {
		return true
	}
	var ce *ClassifyError
	if errors.As(err, &ce) {
		return IsFatalModelError(ce.Err)
	}
	return false
}
