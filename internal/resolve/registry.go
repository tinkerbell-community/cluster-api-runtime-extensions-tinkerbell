package resolve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Registrar uploads schematics to an Image Factory and returns their IDs. Registration
// is idempotent (the factory returns the same ID for the same document), which is what
// makes the content-hash cache safe.
type Registrar struct {
	baseURL string
	client  *http.Client

	mu    sync.Mutex
	cache map[string]string
}

// NewRegistrar builds a Registrar against an Image Factory. An empty factoryURL selects
// the public factory.
func NewRegistrar(factoryURL string) *Registrar {
	if factoryURL == "" {
		factoryURL = DefaultFactoryURL
	}
	return &Registrar{
		baseURL: strings.TrimSuffix(factoryURL, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
		cache:   map[string]string{},
	}
}

// Register uploads a schematic and returns its ID, caching on the document's content hash.
func (r *Registrar) Register(ctx context.Context, schematic Schematic) (string, error) {
	body, err := schematic.Marshal()
	if err != nil {
		return "", err
	}
	key := contentKey(body)

	r.mu.Lock()
	cached, ok := r.cache[key]
	r.mu.Unlock()
	if ok {
		return cached, nil
	}

	id, err := r.upload(ctx, body)
	if err != nil {
		return "", err
	}

	r.mu.Lock()
	r.cache[key] = id
	r.mu.Unlock()
	return id, nil
}

func (r *Registrar) upload(ctx context.Context, body []byte) (string, error) {
	endpoint := r.baseURL + "/schematics"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("building schematic request: %w", err)
	}
	req.Header.Set("Content-Type", "application/yaml")

	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("uploading schematic to %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", fmt.Errorf("reading schematic response: %w", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("image factory returned %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}

	var decoded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", fmt.Errorf("decoding schematic response: %w", err)
	}
	if decoded.ID == "" {
		return "", fmt.Errorf("image factory returned an empty schematic id")
	}
	return decoded.ID, nil
}

func contentKey(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
