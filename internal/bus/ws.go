package bus

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
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

// QoS lanes (whitepaper §5.3): control beats signal beats data beats bulk.
// The lanes exist so a task_cancel or a negotiation yield jumps ahead of the
// artifact stream that would otherwise hold the write slot — the mesh's
// "control plane can always speak" guarantee.
const (
	QoSControl = iota // cancels, declines, grants, yields, hello/heartbeat
	QoSSignal         // task lifecycle: delegate/result/resume/context
	QoSData           // artifact chunks — the bandwidth-heavy plane
	QoSBulk           // everything else
	qosLanes
)

// qosLaneCap bounds each lane's backlog; a full lane back-pressures the
// sender exactly the way the old write mutex did.
const qosLaneCap = 64

// qosForType classifies an envelope by its message type.
func qosForType(typ string) int {
	switch typ {
	case MsgHello, MsgHeartbeat, MsgTaskCancel, MsgTaskDecline,
		MsgAgentGrant, MsgAgentYield, MsgArtifactPushStatus, MsgArtifactPushDone:
		return QoSControl
	case MsgTaskDelegate, MsgTaskAccept, MsgTaskResult, MsgTaskProgress,
		MsgTaskRetry, MsgTaskTransfer, MsgTaskResume,
		MsgContextFetch, MsgContextAck, MsgAgentNegotiate, MsgJoin,
		MsgDTNBundle:
		return QoSSignal
	case MsgArtifactChunk, MsgArtifactFetch, MsgArtifactPush:
		return QoSData
	default:
		return QoSBulk
	}
}

// queuedWrite is one frame awaiting the writer goroutine. res carries the
// real write result back to the sender — Send stays synchronous, only the
// ORDER of writes is prioritized.
type queuedWrite struct {
	data []byte
	res  chan error
}

// Conn wraps one websocket.Conn with a prioritized writer goroutine (the
// single writer goroutine; reads happen on the caller's side).
type Conn struct {
	ws     *websocket.Conn
	idMu   sync.RWMutex
	peerID string // authenticated node id, bound once at hello
	// outbound marks a locally-initiated connection (we dialed); inbound
	// conns (we accepted) leave it false. Peer dedup uses it to pick a
	// deterministic winner between simultaneous mutual dials.
	outbound bool
	logger   *slog.Logger
	// lanes hold pending writes by QoS class; wake nudges the writer that a
	// lane gained work; done closes the writer on Close. doneOnce guards the
	// close: the writer, Close() and the write-error path all converge here.
	lanes    [qosLanes]chan queuedWrite
	wake     chan struct{}
	done     chan struct{}
	doneOnce sync.Once
	// rttNanos is the last measured ping/pong round trip (§4.1 link metric):
	// nanoseconds, zero until the first pong answers a timestamped ping.
	rttNanos atomic.Int64
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

// RemoteAddr returns the peer's transport address as our socket observed it
// — the reflexive-IP hint the hello reply hands back so a NAT-bound peer can
// learn its public address without a STUN server (HelloPayload.You).
func (c *Conn) RemoteAddr() string {
	return c.ws.RemoteAddr().String()
}

func newConn(ws *websocket.Conn, logger *slog.Logger) *Conn {
	ws.SetReadLimit(readLimit)
	c := &Conn{ws: ws, logger: logger, wake: make(chan struct{}, 1), done: make(chan struct{})}
	ws.SetPongHandler(func(data string) error {
		// A ping written by writeLoop carries its send time as 8 bytes of
		// big-endian nanos; answering it gives the link its RTT sample.
		if len(data) == 8 {
			if sent := int64(binary.BigEndian.Uint64([]byte(data))); sent > 0 {
				if rtt := time.Since(time.Unix(0, sent)); rtt > 0 {
					c.rttNanos.Store(int64(rtt))
				}
			}
		}
		return ws.SetReadDeadline(time.Now().Add(pongWait))
	})
	// Initial deadline so a peer that never responds is detected promptly.
	_ = ws.SetReadDeadline(time.Now().Add(pongWait))
	for i := range c.lanes {
		c.lanes[i] = make(chan queuedWrite, qosLaneCap)
	}
	go c.writeLoop()
	return c
}

// StartPingLoop sends a ping every pingPeriod and refreshes the read
// deadline, keeping the connection alive and letting the peer's pong reset
// our deadline. It returns when ctx is done. Pings ride the control lane so
// keepalives are never stuck behind a data backlog.
func (c *Conn) StartPingLoop(ctx context.Context, pingPeriod time.Duration) {
	t := time.NewTicker(pingPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.enqueue(nil, QoSControl); err != nil {
				c.logger.Debug("ping failed", "err", err)
				return
			}
		}
	}
}

// writeLoop is the conn's single writer: it drains the QoS lanes in priority
// order, so a control message queued behind a data backlog is written first.
// Senders block on their own result channel — delivery errors still reach
// the caller that asked, so the outbox's failure semantics are unchanged.
func (c *Conn) writeLoop() {
	for {
		var m queuedWrite
		var ok bool
		for l := 0; l < qosLanes && !ok; l++ {
			select {
			case m = <-c.lanes[l]:
				ok = true
			default:
			}
		}
		if !ok {
			select {
			case <-c.wake:
				continue
			case <-c.done:
				c.failQueued()
				return
			}
		}
		// Bound the write so a wedged peer cannot stall the writer (and
		// thereby every queued sender) forever.
		_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
		var err error
		if m.data == nil {
			var ping [8]byte
			binary.BigEndian.PutUint64(ping[:], uint64(time.Now().UnixNano()))
			err = c.ws.WriteMessage(websocket.PingMessage, ping[:])
		} else {
			err = c.ws.WriteMessage(websocket.TextMessage, m.data)
		}
		m.res <- err
		if err != nil {
			// A dead socket makes every subsequent write fail too; close so
			// pending senders get their error instead of queueing forever.
			_ = c.ws.Close()
			c.failQueued()
			return
		}
	}
}

// failQueued answers every sender still parked in a lane, then closes done
// so late arrivals fail fast instead of queueing behind a dead writer.
func (c *Conn) failQueued() {
	for l := 0; l < qosLanes; l++ {
	drain:
		for {
			select {
			case m := <-c.lanes[l]:
				m.res <- errors.New("bus: connection closed")
			default:
				break drain
			}
		}
	}
	c.doneOnce.Do(func() { close(c.done) })
}

// enqueue marshals v, parks it on its QoS lane and blocks until the writer
// reports the real write result — the same synchronous contract the old
// send-mutex version had. A nil data payload writes a ping frame.
func (c *Conn) enqueue(data []byte, lane int) error {
	m := queuedWrite{data: data, res: make(chan error, 1)}
	select {
	case c.lanes[lane] <- m:
	case <-c.done:
		return errors.New("bus: connection closed")
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
	select {
	case err := <-m.res:
		return err
	case <-c.done:
		// done raced the writer's answer — prefer the real result if it
		// already landed, so a delivered frame is not reported lost.
		select {
		case err := <-m.res:
			return err
		default:
		}
		return errors.New("bus: connection closed")
	}
}

// Send marshals v to JSON and queues it on its QoS lane — an Envelope's own
// type picks the lane, so callers get §5.3 priority without any change.
func (c *Conn) Send(v any) error {
	lane := QoSBulk
	if env, ok := v.(Envelope); ok {
		lane = qosForType(env.Type)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.enqueue(data, lane)
}

// ReadJSON reads one JSON message into v. Callers must set a read deadline
// if they want a timeout.
func (c *Conn) ReadJSON(v any) error {
	return c.ws.ReadJSON(v)
}

// RTT returns the last measured ping/pong round trip on this link, or 0
// before the first sample. It is the edge weight the mesh's weighted
// shortest-path routing consumes (§4.1).
func (c *Conn) RTT() time.Duration {
	return time.Duration(c.rttNanos.Load())
}

// Close closes the underlying socket and fails the writer loop so queued
// senders unblock with an error instead of waiting forever.
func (c *Conn) Close() error {
	c.failQueued()
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
