package broker

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus/client/lookuptable"
	"github.com/mark3labs/mcp-go/mcp"
)

// Present implements port.Presence over mark_presence.
func (g *Gateway) Present(ctx context.Context) ([]domain.Presence, error) {
	raw, err := g.read(ctx, toolRead{Tool: "mark_presence"})
	// The tool takes no parameters, so invalid params is a broker without it.
	if errors.Is(err, mcp.ErrInvalidParams) {
		return nil, domain.ErrPresenceUnsupported
	}
	if err != nil {
		return nil, err
	}
	return parsePresenceTable(raw.Body), nil
}

// parsePresenceTable reads mark_presence's rows (| user | clients | seen |).
// A row whose seen cell is no time, the header and separator among them, is
// skipped.
func parsePresenceTable(text string) []domain.Presence {
	var out []domain.Presence
	for line := range strings.SplitSeq(text, "\n") {
		cells, ok := lookuptable.SplitRow(line)
		if !ok || len(cells) < 3 || cells[0] == "" {
			continue
		}
		seen, err := time.Parse(time.RFC3339, cells[2])
		if err != nil {
			continue
		}
		person := domain.Presence{User: lookuptable.Unescape(cells[0]), Seen: seen}
		for client := range strings.SplitSeq(lookuptable.Unescape(cells[1]), ",") {
			if client = strings.TrimSpace(client); client != "" {
				person.Clients = append(person.Clients, client)
			}
		}
		out = append(out, person)
	}
	return out
}
