package connect

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Flockadoo/flockadoo/pkg/logger"
	"github.com/Flockadoo/flockadoo/pkg/nodeapi"
	"github.com/Flockadoo/flockadoo/pkg/version"
	"github.com/coder/websocket"
	"github.com/hashicorp/yamux"
	"go.uber.org/zap"
)

// ErrInvalidAPIKey is returned when the platform rejects the provider key.
var ErrInvalidAPIKey = errors.New("invalid provider API key")

// Timing paces the reconnects.
type Timing struct {
	BaseDelay time.Duration // the first reconnect delay
	MaxDelay  time.Duration // the delay doubles up to this
	Stable    time.Duration // a session connected this long counts as stable (item Q1)
	// KeepAlive is how often the session is pinged, and KeepAliveTimeout how
	// long a ping may go unanswered: together they bound how long a
	// half-open session goes unnoticed (open question U2).
	KeepAlive        time.Duration
	KeepAliveTimeout time.Duration
	// ReadHeaderTimeout is how long a stream may take to send its request
	// header (finding NET-5).
	ReadHeaderTimeout time.Duration
}

// withDefaults fills the fields a caller left at zero.
func (t Timing) withDefaults() Timing {
	d := DefaultTiming()
	for _, f := range []struct{ v, def *time.Duration }{
		{&t.BaseDelay, &d.BaseDelay}, {&t.MaxDelay, &d.MaxDelay}, {&t.Stable, &d.Stable},
		{&t.KeepAlive, &d.KeepAlive}, {&t.KeepAliveTimeout, &d.KeepAliveTimeout}, {&t.ReadHeaderTimeout, &d.ReadHeaderTimeout},
	} {
		if *f.v <= 0 {
			*f.v = *f.def
		}
	}
	return t
}

// DefaultTiming is the production pacing: 1 s doubling to 32 s, plus up to
// half again in jitter.
func DefaultTiming() Timing {
	return Timing{BaseDelay: time.Second, MaxDelay: 32 * time.Second, Stable: 60 * time.Second,
		KeepAlive: 15 * time.Second, KeepAliveTimeout: 10 * time.Second, ReadHeaderTimeout: 30 * time.Second}
}

// Client keeps one outbound yamux session to connect-gateway.
type Client struct {
	PlatformURL string
	APIKey      string
	Handler     http.Handler
	// OnConnect runs after each successful upgrade, including reconnects.
	OnConnect func()
	// Timing paces the reconnects; New sets DefaultTiming.
	Timing Timing
	// OnState, if set, is called after every change of State.
	OnState func(State)

	// sleep, if set, replaces the wait between dials (see wait).
	sleep func(ctx context.Context, d time.Duration) bool

	connected atomic.Bool
	up        chan error

	mu     sync.Mutex
	state  State
	reason string
}

func New(platformURL, apiKey string, handler http.Handler, onConnect func()) *Client {
	return &Client{
		PlatformURL: platformURL,
		APIKey:      apiKey,
		Handler:     handler,
		OnConnect:   onConnect,
		Timing:      DefaultTiming(),
		up:          make(chan error, 1),
	}
}

func (c *Client) Connected() bool {
	return c.connected.Load()
}

// DialURL turns https://host into wss://host/api/connect/ws.
func DialURL(platform string) (string, error) {
	u, err := url.Parse(platform)
	if err != nil {
		return "", fmt.Errorf("platform url: %w", err)
	}
	switch u.Scheme {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported platform url scheme %q", u.Scheme)
	}
	u.Path = "/api/connect/ws"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// Run dials until ctx is cancelled or the tunnel reaches a terminal state
// (a rejected key, a replacement, an upgrade required), and returns why.
// Other disconnects retry with jittered backoff. The process stays up in a
// terminal state; State and Reason say why (decision DC8).
func (c *Client) Run(ctx context.Context) error {
	wsURL, err := DialURL(c.PlatformURL)
	if err != nil {
		return err
	}
	timing := c.Timing.withDefaults()
	backoff := timing.BaseDelay
	first := true
	short := 0
	reason := ""
	for {
		if ctx.Err() != nil {
			c.setState(Stopped, "")
			return ctx.Err()
		}
		c.setState(Dialing, reason)
		connected, up, err := c.session(ctx, wsURL, timing)
		c.connected.Store(false)
		var terminal State
		switch {
		case errors.Is(err, ErrInvalidAPIKey):
			terminal, reason = Rejected, "Flockadoo rejected this API key; run flockadoo setup with a current key"
		case errors.Is(err, ErrUpgradeRequired):
			terminal, reason = UpgradeRequired, "Flockadoo needs a newer client; run flockadoo update"
		case errors.Is(err, ErrReplaced):
			terminal, reason = Replaced, "another client connected with this API key and replaced this one"
		}
		if terminal != Dialing {
			c.setState(terminal, reason)
			c.notify(err)
			return err
		}
		if first {
			first = false
			c.notify(nil)
		}
		if ctx.Err() != nil {
			c.setState(Stopped, "")
			return ctx.Err()
		}
		logger.Warn("provider connection ended", zap.Error(err), zap.Duration("connected_for", connected))
		// Only a session that stayed up for the stability period resets the
		// delay. A shorter one leaves it growing, so two clients sharing a key
		// cannot replace each other at full speed (findings NET-1, NET-4), and
		// several in a row name that as the likely cause.
		switch {
		case up && connected >= timing.Stable:
			backoff, short, reason = timing.BaseDelay, 0, ""
		case up:
			if short++; short >= shortSessionsBeforeWarning {
				reason = "the connection keeps ending within a minute: another client may be using this API key"
			}
		}
		c.setState(Backoff, reason)
		wait := backoff + time.Duration(rand.Int63n(int64(backoff/2)+1)) //nolint:gosec // G404: reconnect jitter needs no cryptographic randomness
		if !c.wait(ctx, wait) {
			return ctx.Err()
		}
		backoff = min(backoff*2, timing.MaxDelay)
	}
}

func (c *Client) notify(err error) {
	select {
	case c.up <- err:
	default:
	}
}

// Ready waits until the first dial finishes or 401s. A timeout means the
// gateway was unreachable; Run keeps retrying.
func (c *Client) Ready(ctx context.Context) error {
	select {
	case err := <-c.up:
		return err
	case <-ctx.Done():
		return nil
	}
}

// session dials once and serves the session until it ends. It returns how
// long the session was connected, and whether it connected at all: on
// Windows the monotonic clock moves only on the timer tick, so a session the
// gateway ends at once can last zero (seen in CI).
func (c *Client) session(ctx context.Context, wsURL string, timing Timing) (lasted time.Duration, up bool, err error) {
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.APIKey)
	header.Set(nodeapi.HeaderClientVersion, version.Version)
	conn, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{HTTPHeader: header}) //nolint:bodyclose // websocket.Dial owns the body: "You never need to close resp.Body yourself"
	if resp != nil {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return 0, false, ErrInvalidAPIKey
		case http.StatusUpgradeRequired:
			return 0, false, ErrUpgradeRequired
		}
	}
	if err != nil {
		return 0, false, err
	}
	netConn := &closeCatcher{Conn: websocket.NetConn(ctx, conn, websocket.MessageBinary)}
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = timing.KeepAlive
	cfg.ConnectionWriteTimeout = timing.KeepAliveTimeout
	cfg.LogOutput = logWriter{}
	session, err := yamux.Server(netConn, cfg)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "yamux")
		return 0, false, err
	}
	c.connected.Store(true)
	c.setState(Connected, c.Reason())
	start := time.Now()
	c.notify(nil)
	if c.OnConnect != nil {
		go c.OnConnect()
	}
	srv := &http.Server{
		Handler:           c.Handler,
		ReadHeaderTimeout: timing.ReadHeaderTimeout,
		ErrorLog:          log.New(logWriter{}, "", 0),
	}
	err = srv.Serve(session)
	c.connected.Store(false)
	_ = session.Close()
	if websocket.CloseStatus(netConn.err()) == CloseReplaced {
		return time.Since(start), true, ErrReplaced
	}
	return time.Since(start), true, err
}

// closeCatcher remembers the first read error of the connection under the
// session, which carries the gateway's close code.
type closeCatcher struct {
	net.Conn
	mu    sync.Mutex
	first error
}

func (c *closeCatcher) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil {
		c.mu.Lock()
		if c.first == nil {
			c.first = err
		}
		c.mu.Unlock()
	}
	return n, err
}

func (c *closeCatcher) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.first
}

// wait waits d before the next dial. It is the one place Run waits, so the
// tests, which set c.sleep, see each delay Run chose and still wait it.
func (c *Client) wait(ctx context.Context, d time.Duration) bool {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	return sleep(ctx, d)
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
