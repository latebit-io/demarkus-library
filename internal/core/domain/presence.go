package domain

import (
	"errors"
	"time"
)

// ErrPresenceUnsupported is a knowledge system that keeps no presence list:
// a broker older than the tool.
var ErrPresenceUnsupported = errors.New("presence unsupported")

// Presence is one person who used the knowledge system lately and the MCP
// clients seen for them: an agent, or this reading room.
type Presence struct {
	User    string
	Clients []string
	Seen    time.Time
}
