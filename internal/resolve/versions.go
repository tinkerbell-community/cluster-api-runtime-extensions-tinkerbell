package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// versionsCacheTTL is how long a fetched /versions listing is trusted before refresh.
const versionsCacheTTL = 10 * time.Minute

// gaVersionRe matches a General Availability Talos version and rejects any pre-release.
var gaVersionRe = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// minorRe extracts major.minor from a floor written as a bare minor or a full version.
var minorRe = regexp.MustCompile(`^v(\d+)\.(\d+)`)

// VersionResolver answers "what is the newest Talos release" against an Image Factory.
type VersionResolver struct {
	baseURL string
	client  *http.Client
	ttl     time.Duration

	mu        sync.Mutex
	cache     []talosSemver
	fetchedAt time.Time
}

// NewVersionResolver builds a resolver against an Image Factory; empty selects the public one.
func NewVersionResolver(factoryURL string) *VersionResolver {
	if factoryURL == "" {
		factoryURL = DefaultFactoryURL
	}
	return &VersionResolver{
		baseURL: strings.TrimSuffix(factoryURL, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
		ttl:     versionsCacheTTL,
	}
}

// LatestPatch returns the newest GA patch within the minor of floor, or "" when the minor
// has no GA release (callers treat "" as "leave unresolved", not an error).
func (r *VersionResolver) LatestPatch(ctx context.Context, floor string) (string, error) {
	major, minor, ok := parseMinor(floor)
	if !ok {
		return "", nil
	}
	versions, err := r.versions(ctx)
	if err != nil {
		return "", err
	}
	var best talosSemver
	found := false
	for _, v := range versions {
		if v.major != major || v.minor != minor {
			continue
		}
		if !found || best.less(v) {
			best, found = v, true
		}
	}
	if !found {
		return "", nil
	}
	return best.String(), nil
}

// LatestMinor returns the newest GA minor line, skipping minors that are only pre-releases.
func (r *VersionResolver) LatestMinor(ctx context.Context) (string, error) {
	versions, err := r.versions(ctx)
	if err != nil {
		return "", err
	}
	best := talosSemver{}
	found := false
	for _, v := range versions {
		if !found || best.less(v) {
			best, found = v, true
		}
	}
	if !found {
		return "", nil
	}
	return fmt.Sprintf("v%d.%d", best.major, best.minor), nil
}

// versions returns the GA versions the factory can build, refreshing on TTL. The lock is
// held across the fetch so cold-cache reconciles coalesce onto one request (single-flight);
// a fetch error serves a stale listing rather than failing resolution outright.
func (r *VersionResolver) versions(ctx context.Context) ([]talosSemver, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cache != nil && time.Since(r.fetchedAt) < r.ttl {
		return r.cache, nil
	}
	fetched, err := r.fetch(ctx)
	if err != nil {
		if r.cache != nil {
			return r.cache, nil
		}
		return nil, err
	}
	r.cache = fetched
	r.fetchedAt = time.Now()
	return r.cache, nil
}

func (r *VersionResolver) fetch(ctx context.Context) ([]talosSemver, error) {
	endpoint := r.baseURL + "/versions"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("building versions request: %w", err)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("listing versions from %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading versions response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image factory returned %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}
	var raw []string
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("decoding versions response: %w", err)
	}
	out := make([]talosSemver, 0, len(raw))
	for _, s := range raw {
		if v, ok := parseGAVersion(s); ok {
			out = append(out, v)
		}
	}
	return out, nil
}

type talosSemver struct{ major, minor, patch int }

func (v talosSemver) String() string { return fmt.Sprintf("v%d.%d.%d", v.major, v.minor, v.patch) }

func (v talosSemver) less(other talosSemver) bool {
	if v.major != other.major {
		return v.major < other.major
	}
	if v.minor != other.minor {
		return v.minor < other.minor
	}
	return v.patch < other.patch
}

func parseGAVersion(s string) (talosSemver, bool) {
	m := gaVersionRe.FindStringSubmatch(s)
	if m == nil {
		return talosSemver{}, false
	}
	return talosSemver{major: atoi(m[1]), minor: atoi(m[2]), patch: atoi(m[3])}, true
}

func parseMinor(floor string) (major, minor int, ok bool) {
	m := minorRe.FindStringSubmatch(floor)
	if m == nil {
		return 0, 0, false
	}
	return atoi(m[1]), atoi(m[2]), true
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
