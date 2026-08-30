package bus

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Xustalis/OpenPanda/internal/guard"
	"github.com/gorilla/websocket"
)

const (
	// readLimit caps a single message; Phase 0 control messages are tiny.
	readLimit = 4 << 20 // 4 MiB

	// pongWait is how long we wait for a pong before considering the peer
	// dead. The application-level heartbeat (ledger) has a slower cadence;
	// this only guards the TCP/WS layer.
	pongWait = 60 * time.Second

	// pingPeriod is how often we send a ping; must be < pongWait so a
	// healthy peer's pongs keep refreshing our read deadline.
	pingPeriod = 30 * time.Second

	// writeWait bounds how long a write (data or ping) may block.
	writeWait = 10 * time.Second
)

// defaultHelloTimeout is how long an inbound connection has to send a valid
// hello before the server drops it. This bounds slow-/never-handshake DoS.
const defaultHelloTimeout = 10 * time.Second

// Conn wraps one websocket.Conn with a send mutex (one writer goroutine).
type Conn struct {
	ws     *websocket.Conn
	mu     sync.Mutex // serializes writes
	idMu   sync.RWMutex
	peerID string // authenticated node id, bound once at hello
	// outbound marks a locally-initiated connection (we dialed); inbound
	// conns (we accepted) leave it false. Peer dedup uses it to pick a
	// deterministic winner between simultaneous mutual dials.
	outbound bool
	logger   *slog.Logger
}

// SetPeerID binds the authenticated node id to this connection (set once, at
// hello). PeerID returns it, or "" before the handshake completes.
func (c *Conn) SetPeerID(id string) {
	c.idMu.Lock()
	c.peerID = id
	c.idMu.Unlock()
}

// PeerID returns the node id bound to this connection by the hello handshake.
func (c *Conn) PeerID() string {
	c.idMu.RLock()
	defer c.idMu.RUnlock()
	return c.peerID
}

// MarkOutbound flags this connection as locally-initiated; Outbound reports it.
func (c *Conn) MarkOutbound() {
	c.idMu.Lock()
	c.outbound = true
	c.idMu.Unlock()
}

// Outbound reports whether this connection was initiated by this side.
func (c *Conn) Outbound() bool {
	c.idMu.RLock()
	defer c.idMu.RUnlock()
	return c.outbound
}

func newConn(ws *websocket.Conn, logger *slog.Logger) *Conn {
	ws.SetReadLimit(readLimit)
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(pongWait))
	})
	// Initial deadline so a peer that never responds is detected promptly.
	_ = ws.SetReadDeadline(time.Now().Add(pongWait))
	return &Conn{ws: ws, logger: logger}
}

// StartPingLoop sends a ping every pingPeriod and refreshes the read
// deadline, keeping the connection alive and letting the peer's pong reset
// our deadline. It returns when ctx is done. Runs on the single-writer
// mutex so it cannot race data messages.
func (c *Conn) StartPingLoop(ctx context.Context, pingPeriod time.Duration) {
	t := time.NewTicker(pingPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.mu.Lock()
			// Refreshing the write deadline protects against a wedged peer.
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				c.mu.Unlock()
				c.logger.Debug("ping failed", "err", err)
				return
			}
			c.mu.Unlock()
		}
	}
}

// Send marshals v to JSON and writes it to the peer.
func (c *Conn) Send(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Bound the write so a wedged peer cannot block this (and, via the write
	// mutex, every other) writer forever.
	_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
	return c.ws.WriteJSON(v)
}

// ReadJSON reads one JSON message into v. Callers must set a read deadline
// if they want a timeout.
func (c *Conn) ReadJSON(v any) error {
	return c.ws.ReadJSON(v)
}

// Close closes the underlying socket.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.Close()
}

// ResetReadDeadline restores the normal keepalive read deadline (pongWait).
// Callers should invoke this once a hello handshake succeeds.
func (c *Conn) ResetReadDeadline() error {
	return c.ws.SetReadDeadline(time.Now().Add(pongWait))
}

// Server listens for inbound node connections.
type Server struct {
	addr     string
	logger   *slog.Logger
	onConn   func(*Conn, string) // called with (conn, nodeID) after hello; must block while the conn is alive
	upgrader websocket.Upgrader

	maxConns      int
	maxConnsPerIP int
	mu            sync.Mutex
	active        int
	activePerIP   map[string]int

	helloTimeout time.Duration // per-server hello deadline; tests can shorten it
}

// NewServer creates a WebSocket server on addr. onConn is invoked once a
// peer handshakes and identifies itself. onConn must block while the connection
// is alive so the server can accurately enforce connection limits.
func NewServer(addr string, logger *slog.Logger, onConn func(*Conn, string)) *Server {
	return &Server{
		addr:   addr,
		logger: logger,
		onConn: onConn,
		upgrader: websocket.Upgrader{
			// Node-to-node Go clients send no Origin header; a browser would.
			// Reject any Origin so a cross-site page cannot open a WebSocket to
			// this node's control channel (the PWA talks HTTP on the panel port,
			// never here).
			CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
		},
		activePerIP:  make(map[string]int),
		helloTimeout: defaultHelloTimeout,
	}
}

// SetLimits configures the global and per-IP concurrent connection limits.
// A limit <= 0 means unlimited.
func (s *Server) SetLimits(maxConns, maxConnsPerIP int) {
	s.mu.Lock()
	s.maxConns = maxConns
	s.maxConnsPerIP = maxConnsPerIP
	s.mu.Unlock()
}

// Listen blocks serving WebSocket requests until ctx is done.
func (s *Server) Listen(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handle)
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("websocket listening", "addr", s.addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		s.logger.Info("websocket server stopping")
		shCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return srv.Shutdown(shCtx)
	case err := <-errCh:
		return err
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}

	s.mu.Lock()
	if s.maxConns > 0 && s.active >= s.maxConns {
		s.mu.Unlock()
		s.logger.Warn("connection limit reached", "remote", r.RemoteAddr)
		http.Error(w, "too many connections", http.StatusServiceUnavailable)
		return
	}
	if s.maxConnsPerIP > 0 && s.activePerIP[ip] >= s.maxConnsPerIP {
		s.mu.Unlock()
		s.logger.Warn("per-IP connection limit reached", "remote", r.RemoteAddr, "ip", ip)
		http.Error(w, "too many connections from this IP", http.StatusServiceUnavailable)
		return
	}
	s.active++
	s.activePerIP[ip]++
	s.mu.Unlock()

	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.dec(ip)
		s.logger.Warn("upgrade failed", "err", err)
		return
	}
	conn := newConn(ws, s.logger)
	// The first message from a peer is hello; give it a short, bounded window
	// to authenticate before the normal pong/keepalive deadline takes over.
	_ = conn.ws.SetReadDeadline(time.Now().Add(s.helloTimeout))
	s.logger.Info("inbound connection", "remote", r.RemoteAddr)
	// The Core loop drives reads via a Reader; onConn must block while the
	// connection is alive so the server can accurately account for limits.
	// Guarded synchronously (not guard.Go): a panic in one connection's read
	// loop is logged and closes only that connection — a hostile or buggy peer
	// must not be able to crash the whole node.
	guard.Call(s.logger, "bus: conn read loop "+r.RemoteAddr, func() { _ = conn.Close() }, func() {
		s.onConn(conn, "")
	})
	s.dec(ip)
}

func (s *Server) dec(ip string) {
	s.mu.Lock()
	s.active--
	if s.active < 0 {
		s.active = 0
	}
	s.activePerIP[ip]--
	if s.activePerIP[ip] <= 0 {
		delete(s.activePerIP, ip)
	}
	s.mu.Unlock()
}

// Client dials a peer and returns a connected Conn (after hello exchange is
// handled by the caller).
type Client struct {
	url    string
	logger *slog.Logger
	dialer *websocket.Dialer
}

// NewClient creates a client for a ws:// or wss:// endpoint.
func NewClient(url string, logger *slog.Logger) *Client {
	return &Client{
		url:    url,
		logger: logger,
		dialer: &websocket.Dialer{
			HandshakeTimeout: 10 * time.Second,
			TLSClientConfig:  &tls.Config{InsecureSkipVerify: false},
		},
	}
}

// Dial establishes a connection. Callers should then send hello and start
// reading.
func (c *Client) Dial(ctx context.Context) (*Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ws, _, err := c.dialer.DialContext(dialCtx, c.url, nil)
	if err != nil {
		return nil, err
	}
	return newConn(ws, c.logger), nil
}
