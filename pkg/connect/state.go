package connect

// State is where the tunnel is (plan item C1, section 6.4). Not written yet:
// tests first. The stub knows only connected and dialing.
type State int

const (
	Dialing State = iota
	Connected
	Backoff
	Rejected
	Replaced
	UpgradeRequired
	Stopped
)

// CloseReplaced is the WebSocket close code the gateway sends when a newer
// session with the same key replaces this one (decision DC8).
const CloseReplaced = 4001

// Terminal is not written yet.
func (s State) Terminal() bool { return false }

// State is not written yet.
func (c *Client) State() State {
	if c.connected.Load() {
		return Connected
	}
	return Dialing
}

// Reason is not written yet.
func (c *Client) Reason() string { return "" }
