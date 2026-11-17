package connect

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sentnl/inferoute-node/inferoute-client/pkg/logger"
	"go.uber.org/zap"
)

// State is where the tunnel is (plan item C1, section 6.4). One goroutine,
// Run, moves it.
type State int

const (
	Dialing         State = iota // opening a session
	Connected                    // a session is up
	Backoff                      // waiting before the next dial
	Rejected                     // terminal: the platform refused the key
	Replaced                     // terminal: another client took the key
	UpgradeRequired              // terminal: this client version is too old
	Stopped                      // Run has returned
)

func (s State) String() string {
	switch s {
	case Dialing:
		return "dialing"
	case Connected:
		return "connected"
	case Backoff:
		return "backoff"
	case Rejected:
		return "rejected"
	case Replaced:
		return "replaced"
	case UpgradeRequired:
		return "upgrade_required"
	case Stopped:
		return "stopped"
	default:
		return fmt.Sprintf("state(%d)", int(s))
	}
}

// Terminal states end the tunnel for good: the client stops dialling and
// reporting, stays up, and shows why (decision DC8).
func (s State) Terminal() bool {
	switch s {
	case Rejected, Replaced, UpgradeRequired:
		return true
	case Dialing, Connected, Backoff, Stopped:
		return false
	default:
		return false
	}
}

// CloseReplaced is the WebSocket close code the gateway sends when a newer
// session with the same key replaces this one (decision DC8). The node does
// not send it yet; until it does, the client infers a replacement from
// sessions that keep ending early.
const CloseReplaced = 4001

// shortSessionsBeforeWarning is how many sessions in a row must end before
// the stability period before the client names another client as the
// likely cause.
const shortSessionsBeforeWarning = 3

var (
	// ErrUpgradeRequired: the gateway answered the dial with 426.
	ErrUpgradeRequired = errors.New("this client version is too old for Inferoute")
	// ErrReplaced: the gateway closed the session for a newer one.
	ErrReplaced = errors.New("another client connected with this API key")
)

// State is where the tunnel is now.
func (c *Client) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Reason says why the tunnel is where it is, for the dashboard; empty when
// there is nothing to say.
func (c *Client) Reason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

func (c *Client) setState(s State, reason string) {
	c.mu.Lock()
	changed := c.state != s || c.reason != reason
	c.state, c.reason = s, reason
	c.mu.Unlock()
	if changed && s.Terminal() {
		logger.Error("provider connection stopped for good", zap.String("state", s.String()), zap.String("reason", reason))
	}
}

// logWriter sends a library's own log lines (net/http, yamux) to the log
// file instead of the terminal the dashboard is drawn on (finding NET-5).
type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	if msg := strings.TrimSpace(string(p)); msg != "" {
		logger.Warn(msg)
	}
	return len(p), nil
}
