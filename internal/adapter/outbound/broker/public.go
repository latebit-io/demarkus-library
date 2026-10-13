package broker

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/latebit-io/demarkus-library/internal/core/domain"
	"github.com/latebit-io/demarkus/protocol"
)

// PublicReader reads the hub's branding documents from the broker's public
// route, GET /worlds/<hub>/.well-known/library/<doc>, with no bearer: a cold
// pod and the sign-in page resolve the room before anyone signs in.
type PublicReader struct {
	base string
	http *http.Client
}

// NewPublicReader builds the reader over the broker origin; a nil httpClient
// gets a DefaultTimeout client.
func NewPublicReader(brokerURL string, httpClient *http.Client) *PublicReader {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultTimeout}
	}
	return &PublicReader{base: strings.TrimRight(brokerURL, "/"), http: httpClient}
}

// Raw fetches one document; a 404 is domain.ErrNotFound, which also covers a
// broker without the route, so callers can fall back to a bearer read.
func (r *PublicReader) Raw(ctx context.Context, world, path string) (domain.RawDocument, error) {
	url := r.base + "/worlds/" + world + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return domain.RawDocument{}, fmt.Errorf("public read %s: %w", url, err)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return domain.RawDocument{}, fmt.Errorf("public read %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return domain.RawDocument{}, domain.ErrNotFound
	default:
		return domain.RawDocument{}, fmt.Errorf("public read %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, protocol.MaxBodyLength))
	if err != nil {
		return domain.RawDocument{}, fmt.Errorf("public read %s: %w", url, err)
	}
	return domain.RawDocument{Body: string(body)}, nil
}
