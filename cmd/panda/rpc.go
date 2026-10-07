// SPDX-License-Identifier: AGPL-3.0-or-later

package main

// `panda rpc` — a newline-delimited-JSON protocol over stdin/stdout for
// embedding OpenPanda into other processes (the "RPC mode" pi exposes and
// SDK consumers wrap). One request is served at a time: ask semantics are
// inherently sequential per session, and serialization keeps ordering
// guarantees trivial for callers.
//
// Wire format:
//
//	request:  {"id": <any JSON value>, "method": "<name>", "params": {...}}
//	event:    {"id": <same>, "event": "delta"|"status"|"reasoning", "data": {...}}
//	response: {"id": <same>, "result": {...}}   or   {"id": <same>, "error": {"message": "..."}}
//
// Methods:
//
//	status                                  → {version, model, work_path, sessions_dir}
//	ask {prompt, session_id?, work_dir?, authorize?}
//	                                        → events, then result = ask outcome
//	session.list {}                         → [session]
//	session.get {id}                        → session
//	session.new {title?, project?}          → session
//	session.fork {id, at?}                  → child session
//	convo.get {}                            → bare-mode conversation turns

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"os"

	"github.com/Xustalis/OpenPanda/internal/askengine"
	"github.com/Xustalis/OpenPanda/internal/config"
	"github.com/Xustalis/OpenPanda/internal/entry"
	"github.com/Xustalis/OpenPanda/internal/i18n"
	"github.com/Xustalis/OpenPanda/internal/sessions"
	versionpkg "github.com/Xustalis/OpenPanda/internal/version"
)

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcServer struct {
	cfg        *config.Config
	configPath string
	cardPath   string
	mcpCmd     string
	store      *sessions.Store
	engine     *askengine.Engine // built lazily on the first ask
	out        *bufio.Writer
}

func runRPC(args []string) {
	fs := flag.NewFlagSet("rpc", flag.ExitOnError)
	configPath := fs.String("config", cliConfigPath, "path to config.yaml")
	cardPath := fs.String("card", cardFlagDefault(), "path to capabilities.yaml")
	mcpCmd := fs.String("mcp", cliMCP, "MCP server command (space-separated)")
	fs.Parse(args)
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load config", err)
	}
	srv := &rpcServer{
		cfg:        cfg,
		configPath: *configPath,
		cardPath:   *cardPath,
		mcpCmd:     *mcpCmd,
		store:      sessions.NewStore(sessionStoreRoot(cfg)),
		out:        bufio.NewWriter(os.Stdout),
	}
	defer srv.out.Flush()
	defer func() {
		if srv.engine != nil {
			srv.engine.Close()
		}
	}()

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			srv.replyError(nil, "invalid JSON request")
			continue
		}
		srv.dispatch(&req)
	}
}

func (s *rpcServer) dispatch(req *rpcRequest) {
	switch req.Method {
	case "status":
		s.reply(req.ID, map[string]any{
			"version":      versionpkg.Version,
			"model":        s.cfg.Model.Model,
			"work_path":    s.cfg.Storage.WorkPath,
			"sessions_dir": sessionStoreRoot(s.cfg),
		})
	case "session.list":
		list, err := s.store.List()
		if err != nil {
			s.replyError(req.ID, err.Error())
			return
		}
		if list == nil {
			list = []*sessions.Session{}
		}
		s.reply(req.ID, list)
	case "session.get":
		var p struct {
			ID string `json:"id"`
		}
		if !s.params(req, &p) {
			return
		}
		sess, err := s.store.Get(p.ID)
		if err != nil {
			s.replyError(req.ID, "no such session")
			return
		}
		s.reply(req.ID, sess)
	case "session.new":
		var p struct {
			Title   string `json:"title"`
			Project string `json:"project"`
		}
		if !s.params(req, &p) {
			return
		}
		sess, err := s.store.Create(p.Title, p.Project)
		if err != nil {
			s.replyError(req.ID, err.Error())
			return
		}
		if wt := openWorktreesBestEffort(s.cfg.Storage.WorkPath); wt != nil {
			if path, err := wt.Ensure(context.Background(), sess.ID); err == nil {
				_ = s.store.SetWorktree(sess.ID, path, sessions.Branch(sess.ID))
				sess, _ = s.store.Get(sess.ID)
			}
		}
		s.reply(req.ID, sess)
	case "session.fork":
		var p struct {
			ID string `json:"id"`
			At int    `json:"at"`
		}
		if !s.params(req, &p) {
			return
		}
		parent, err := s.store.Get(p.ID)
		if err != nil {
			s.replyError(req.ID, "no such session")
			return
		}
		child, err := s.store.Fork(p.ID, p.At)
		if err != nil {
			s.replyError(req.ID, err.Error())
			return
		}
		if wt := openWorktreesBestEffort(s.cfg.Storage.WorkPath); wt != nil {
			base := "HEAD"
			if parent.Branch != "" {
				base = parent.Branch
			}
			if path, err := wt.EnsureFrom(context.Background(), child.ID, base); err == nil {
				_ = s.store.SetWorktree(child.ID, path, sessions.Branch(child.ID))
				child, _ = s.store.Get(child.ID)
			}
		}
		s.reply(req.ID, child)
	case "convo.get":
		s.reply(req.ID, map[string]any{"turns": loadConvo()})
	case "ask":
		s.handleAsk(req)
	default:
		s.replyError(req.ID, "unknown method: "+req.Method)
	}
}

// params decodes the request's params into dst; a malformed params object is
// a protocol error, reported to the caller rather than silently defaulted.
func (s *rpcServer) params(req *rpcRequest, dst any) bool {
	if len(req.Params) == 0 {
		return true
	}
	if err := json.Unmarshal(req.Params, dst); err != nil {
		s.replyError(req.ID, "invalid params: "+err.Error())
		return false
	}
	return true
}

func (s *rpcServer) ensureEngine() (*askengine.Engine, error) {
	if s.engine != nil {
		return s.engine, nil
	}
	engine, err := askengine.New(context.Background(), s.cfg, askengine.Options{
		CardPath:   s.cardPath,
		MCPCommand: s.mcpCmd,
		ConfigPath: s.configPath,
		AsyncPeers: true,
	})
	if err != nil {
		return nil, err
	}
	s.engine = engine
	return engine, nil
}

// handleAsk runs one prompt, emitting stream events as they arrive and the
// converged result last. With session_id the exchange persists into that
// thread exactly like `panda session ask` (user turn first, outcome last —
// including the error side on failure).
func (s *rpcServer) handleAsk(req *rpcRequest) {
	var p struct {
		Prompt    string `json:"prompt"`
		SessionID string `json:"session_id"`
		WorkDir   string `json:"work_dir"`
		Authorize bool   `json:"authorize"`
	}
	if !s.params(req, &p) {
		return
	}
	if p.Prompt == "" {
		s.replyError(req.ID, "prompt must not be empty")
		return
	}
	engine, err := s.ensureEngine()
	if err != nil {
		s.replyError(req.ID, err.Error())
		return
	}
	ctx := context.Background()
	send := func(event string, data any) {
		s.event(req.ID, event, data)
	}

	var history []entry.Turn
	var sess *sessions.Session
	workDir := p.WorkDir
	if p.SessionID != "" {
		sess, err = s.store.Get(p.SessionID)
		if err != nil {
			s.replyError(req.ID, "no such session")
			return
		}
		sess, history = sessionHistory(ctx, s.store, sess, engine)
		if _, err := s.store.AppendTurn(sess.ID, sessions.Turn{Role: "user", Text: p.Prompt}); err != nil {
			s.replyError(req.ID, "save turn: "+err.Error())
			return
		}
		if workDir == "" {
			workDir = sess.Worktree
		}
	}
	if workDir == "" {
		workDir = engine.WorkPath()
	}
	// The engine is reused across requests: always (re)bind the project so a
	// bare ask after a project-bound one does not inherit the stale binding.
	if sess != nil && sess.Project != "" {
		engine.SetProject(sess.Project, workDir)
	} else {
		engine.SetProject("", "")
	}

	cb := askengine.StreamCallbacks{
		OnDelta:     func(chunk string) { send("delta", map[string]string{"text": chunk}) },
		OnReasoning: func(text string) { send("reasoning", map[string]string{"text": text}) },
		OnStatus:    func(text string) { send("status", map[string]string{"text": text}) },
	}
	out, err := engine.AskTurnsSession(ctx, history, p.Prompt, workDir, "", sessID(sess), p.Authorize, cb)
	if err != nil {
		if sess != nil {
			_, _ = s.store.AppendTurn(sess.ID, sessions.Turn{Role: "assistant", Text: "⚠ " + err.Error(), Kind: "error"})
		}
		s.replyError(req.ID, err.Error())
		return
	}
	if sess != nil {
		turn := sessions.Turn{Role: "assistant", Kind: out.Kind, Text: convoSummaryOf(i18n.Detect(), out)}
		switch out.Kind {
		case "task":
			turn.Ref = out.TaskID
		case "plan":
			turn.Ref = out.PlanID
		}
		_, _ = s.store.AppendTurn(sess.ID, turn)
	}
	s.reply(req.ID, out)
}

func sessID(sess *sessions.Session) string {
	if sess == nil {
		return ""
	}
	return sess.ID
}

func (s *rpcServer) reply(id json.RawMessage, result any) {
	s.write(map[string]any{"id": id, "result": result})
}

func (s *rpcServer) replyError(id json.RawMessage, msg string) {
	s.write(map[string]any{"id": id, "error": map[string]string{"message": msg}})
}

func (s *rpcServer) event(id json.RawMessage, name string, data any) {
	s.write(map[string]any{"id": id, "event": name, "data": data})
}

func (s *rpcServer) write(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = s.out.Write(data)
	_ = s.out.WriteByte('\n')
	_ = s.out.Flush()
}
