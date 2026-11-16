// SPDX-License-Identifier: AGPL-3.0-or-later
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/mcpserve"
)

// Child operations must enter the live kernel: ordinary task add cannot
// inherit orchestration bounds or release the executing parent's resources.
func (d *selfToolsDeps) toolSubtaskSpawn(ctx context.Context, args map[string]any) (string, error) {
	title := strings.TrimSpace(mcpserve.Arg(args, "title"))
	if title == "" {
		return "", fmt.Errorf("title required")
	}
	requestID := strings.TrimSpace(mcpserve.Arg(args, "request_id"))
	if requestID == "" {
		raw, _ := json.Marshal(args)
		digest := sha256.Sum256(raw)
		requestID = hex.EncodeToString(digest[:])
	}
	tokens, err := childIntegerArg(args, "token_budget", 0, 1<<53-1)
	if err != nil {
		return "", err
	}
	hops, err := childIntegerArg(args, "delegation_budget", 0, 1<<31-1)
	if err != nil {
		return "", err
	}
	return d.callChildRPC(ctx, core.ChildRPCRequest{Operation: "spawn", RequestID: requestID, ParentID: strings.TrimSpace(mcpserve.Arg(args, "parent_id")), Title: title, Prompt: mcpserve.Arg(args, "prompt"), Requires: mcpserve.ArgList(args, "requires"), Project: strings.TrimSpace(mcpserve.Arg(args, "project")), Preferred: strings.TrimSpace(mcpserve.Arg(args, "preferred")), Node: strings.TrimSpace(mcpserve.Arg(args, "node")), Paths: mcpserve.ArgList(args, "paths"), Isolate: args["isolate"] == true, InputVersion: strings.TrimSpace(mcpserve.Arg(args, "input_version")), TokenBudget: tokens, DelegationBudget: int(hops)})
}

func childIntegerArg(args map[string]any, key string, fallback, limit int64) (int64, error) {
	v, exists := args[key]
	if !exists {
		return fallback, nil
	}
	var n float64
	switch value := v.(type) {
	case int:
		n = float64(value)
	case int64:
		n = float64(value)
	case float64:
		n = value
	case json.Number:
		parsed, err := value.Int64()
		if err != nil || parsed < 0 || parsed > limit {
			return 0, fmt.Errorf("%s must be an integer in 0..%d", key, limit)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > float64(limit) || math.Trunc(n) != n {
		return 0, fmt.Errorf("%s must be an integer in 0..%d", key, limit)
	}
	return int64(n), nil
}
func (d *selfToolsDeps) toolSubtaskAwait(ctx context.Context, args map[string]any) (string, error) {
	taskID := strings.TrimSpace(mcpserve.Arg(args, "task_id"))
	if taskID == "" {
		return "", fmt.Errorf("task_id required")
	}
	timeout, err := childIntegerArg(args, "timeout_s", 60, 300)
	if err != nil {
		return "", err
	}
	return d.callChildRPC(ctx, core.ChildRPCRequest{Operation: "await", TaskID: taskID, TimeoutS: int(timeout)})
}
func (d *selfToolsDeps) toolSubtaskCancel(ctx context.Context, args map[string]any) (string, error) {
	taskID := strings.TrimSpace(mcpserve.Arg(args, "task_id"))
	if taskID == "" {
		return "", fmt.Errorf("task_id required")
	}
	return d.callChildRPC(ctx, core.ChildRPCRequest{Operation: "cancel", TaskID: taskID})
}
func (d *selfToolsDeps) callChildRPC(ctx context.Context, req core.ChildRPCRequest) (string, error) {
	endpoint, token, attempt := os.Getenv("PANDA_CHILD_RPC_URL"), os.Getenv("PANDA_CHILD_RPC_TOKEN"), os.Getenv("PANDA_ATTEMPT_ID")
	if endpoint == "" || token == "" || attempt == "" {
		return "", fmt.Errorf("live parent orchestration endpoint unavailable; child operations require an executing Panda task")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		return "", fmt.Errorf("invalid parent loopback endpoint")
	}
	req.AttemptID = attempt
	if req.ParentID == "" {
		req.ParentID = os.Getenv("PANDA_TASK_ID")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/children", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 6 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("parent child operation unavailable: %w", err)
	}
	defer response.Body.Close()
	defer client.CloseIdleConnections()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("child operation refused: %s", strings.TrimSpace(string(body)))
	}
	return string(body), nil
}
