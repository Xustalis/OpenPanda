// SPDX-License-Identifier: AGPL-3.0-or-later

package tsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/taskproviders"
)

var ErrCursorExpired = taskproviders.ErrCursorExpired

type HTTPError struct {
	Status     int
	Code       string
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("task integration HTTP %d (%s)", e.Status, e.Code)
}
func (e *HTTPError) Is(target error) bool {
	return e.Status == 401 && target == taskproviders.ErrReconnect || e.Status == 410 && target == ErrCursorExpired || e.Status == 409 && target == taskproviders.ErrConflict
}

type Client struct {
	Root  string
	HTTP  *http.Client
	Token func(context.Context) (string, error)
}

func New(root string) (*Client, error) {
	if _, err := taskproviders.Endpoint(root, "v1/capabilities"); err != nil {
		return nil, err
	}
	u, _ := url.Parse(root)
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())) {
		return nil, errors.New("task connection requires HTTPS; HTTP is allowed only on loopback")
	}
	return &Client{Root: strings.TrimRight(u.String(), "/"), HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) request(ctx context.Context, method, path string, query url.Values, body any, key, revision string, out any) error {
	endpoint, err := taskproviders.Endpoint(c.Root, path)
	if err != nil {
		return err
	}
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var b []byte
	if body != nil {
		b, err = JSONBytes(body)
		if err != nil {
			return err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Accept", "application/json")
	if c.Token != nil {
		token, err := c.Token(ctx)
		if err != nil {
			return err
		}
		if token == "" {
			return taskproviders.ErrReconnect
		}
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	if revision != "" {
		r.Header.Set("If-Match", `"`+revision+`"`)
	}
	return c.do(r, out)
}
func (c *Client) do(r *http.Request, out any) error {
	client := *c.HTTP
	// Never forward credentials or device proofs through a redirect, including
	// when tests/callers supply a client with the default redirect policy.
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(r)
	if err != nil {
		return errors.New("task integration transport failed")
	}
	defer resp.Body.Close()
	const maxBody = 8 << 20
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return errors.New("task integration response interrupted")
	}
	if len(b) > maxBody {
		return errors.New("task integration response exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var problem struct {
			Error json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(b, &problem)
		var code string
		if json.Unmarshal(problem.Error, &code) != nil {
			var nested struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(problem.Error, &nested)
			code = nested.Code
		}
		// Error descriptions can echo form values. Only expose a bounded code.
		if len(code) > 80 || strings.ContainsAny(code, " \r\n\t<>\"&") {
			code = "invalid_response"
		}
		wait := time.Duration(0)
		if n, err := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64); err == nil && n > 0 && n <= 86400 {
			wait = time.Duration(n) * time.Second
		} else if d, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil {
			wait = time.Until(d)
			if wait < 0 {
				wait = 0
			}
		}
		return &HTTPError{Status: resp.StatusCode, Code: code, RetryAfter: wait}
	}
	if out == nil {
		return nil
	}
	if err = json.Unmarshal(b, out); err != nil {
		return errors.New("invalid task integration response")
	}
	return nil
}
func (c *Client) Capabilities(ctx context.Context) (Capabilities, error) {
	var v Capabilities
	err := c.request(ctx, "GET", "v1/capabilities", nil, nil, "", "", &v)
	return v, err
}
func (c *Client) Me(ctx context.Context) (Me, error) {
	var v Me
	err := c.request(ctx, "GET", "v1/integration/me", nil, nil, "", "", &v)
	return v, err
}
func (c *Client) Spaces(ctx context.Context) (SpaceList, error) {
	var v SpaceList
	err := c.request(ctx, "GET", "v1/integration/spaces", nil, nil, "", "", &v)
	return v, err
}
func (c *Client) Members(ctx context.Context, space, token string) (MemberPage, error) {
	var v MemberPage
	q := url.Values{}
	if token != "" {
		q.Set("pageToken", token)
	}
	err := c.request(ctx, "GET", "v1/integration/spaces/"+url.PathEscape(space)+"/members", q, nil, "", "", &v)
	return v, err
}
func (c *Client) BeginSnapshot(ctx context.Context, space string) (Manifest, error) {
	var v Manifest
	err := c.request(ctx, "POST", "v1/integration/spaces/"+url.PathEscape(space)+"/snapshots", nil, map[string]int{"pageSize": 200}, "", "", &v)
	return v, err
}
func (c *Client) Page(ctx context.Context, id, token string) (Page, error) {
	var v Page
	q := url.Values{}
	if token != "" {
		q.Set("pageToken", token)
	}
	err := c.request(ctx, "GET", "v1/integration/snapshots/"+url.PathEscape(id)+"/pages", q, nil, "", "", &v)
	return v, err
}
func (c *Client) Changes(ctx context.Context, space, cursor string) (Changes, error) {
	var v Changes
	err := c.request(ctx, "POST", "v1/integration/spaces/"+url.PathEscape(space)+"/changes", nil, map[string]any{"cursor": cursor, "limit": 200}, "", "", &v)
	return v, err
}
func (c *Client) Task(ctx context.Context, key taskproviders.Key) (Task, error) {
	var v Task
	if err := key.Validate(); err != nil {
		return v, err
	}
	err := c.request(ctx, "GET", "v1/integration/tasks/"+url.PathEscape(key.TaskID), nil, nil, "", "", &v)
	if err == nil && v.Key != key {
		err = errors.New("task identity mismatch")
	}
	return v, err
}
