package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPFetcher retrieves pinned artefacts over HTTPS for bundle installation.
// It forbids non-HTTPS URLs and does not follow redirects, so a moved or
// downgraded artefact fails closed rather than silently installing.
type HTTPFetcher struct {
	Client *http.Client
}

func (h HTTPFetcher) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirects are not permitted for pinned artefacts")
		},
	}
}

// Fetch downloads a pinned artifact body.
func (h HTTPFetcher) Fetch(ctx context.Context, url string) (io.ReadCloser, error) {
	if len(url) < 8 || url[:8] != "https://" {
		return nil, fmt.Errorf("refusing non-HTTPS artefact URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d fetching artefact", resp.StatusCode)
	}
	return resp.Body, nil
}
