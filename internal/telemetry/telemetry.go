package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kunchenguid/ezoss/internal/buildinfo"
)

const (
	defaultHostname = "cli"
	defaultTitle    = "ezoss CLI"
	defaultPath     = "/api/send"
	umamiCloudURL   = "https://cloud.umami.is"

	umamiHostEnv      = "EZOSS_UMAMI_HOST"
	umamiWebsiteIDEnv = "EZOSS_UMAMI_WEBSITE_ID"
)

type Fields map[string]any

type Sink interface {
	Track(name string, fields Fields)
	Pageview(path string, fields Fields)
	Close(ctx context.Context) error
}

type Config struct {
	Host       string
	WebsiteID  string
	App        string
	Version    string
	GOOS       string
	GOARCH     string
	HTTPClient *http.Client
}

type Client struct {
	endpoint   string
	websiteID  string
	app        string
	version    string
	goos       string
	goarch     string
	httpClient *http.Client

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

type noopSink struct{}

type collectRequest struct {
	Type    string         `json:"type"`
	Payload collectPayload `json:"payload"`
}

type collectPayload struct {
	Website   string         `json:"website"`
	Hostname  string         `json:"hostname"`
	Title     string         `json:"title"`
	URL       string         `json:"url"`
	Name      string         `json:"name,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	Timestamp int64          `json:"timestamp,omitempty"`
}

var (
	defaultMu   sync.Mutex
	defaultSink Sink
)

func NewClient(cfg Config) (*Client, error) {
	endpoint, err := normalizeEndpoint(cfg.Host)
	if err != nil {
		return nil, err
	}
	if cfg.WebsiteID == "" {
		return nil, fmt.Errorf("website ID is required")
	}
	if cfg.App == "" {
		cfg.App = "ezoss"
	}
	if cfg.Version == "" {
		cfg.Version = buildinfo.CurrentVersion()
	}
	if cfg.GOOS == "" {
		cfg.GOOS = runtime.GOOS
	}
	if cfg.GOARCH == "" {
		cfg.GOARCH = runtime.GOARCH
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: time.Second}
	}

	return &Client{
		endpoint:   endpoint,
		websiteID:  cfg.WebsiteID,
		app:        cfg.App,
		version:    cfg.Version,
		goos:       cfg.GOOS,
		goarch:     cfg.GOARCH,
		httpClient: cfg.HTTPClient,
	}, nil
}

func Default() Sink {
	defaultMu.Lock()
	defer defaultMu.Unlock()

	if defaultSink != nil {
		return defaultSink
	}

	websiteID, ok := os.LookupEnv(umamiWebsiteIDEnv)
	websiteID = strings.TrimSpace(websiteID)
	if !ok {
		websiteID = strings.TrimSpace(buildinfo.TelemetryWebsiteID)
	}
	if websiteID == "" {
		defaultSink = noopSink{}
		return defaultSink
	}

	host := strings.TrimSpace(os.Getenv(umamiHostEnv))
	if host == "" {
		host = umamiCloudURL
	}

	client, err := NewClient(Config{
		Host:      host,
		WebsiteID: websiteID,
		App:       "ezoss",
	})
	if err != nil {
		defaultSink = noopSink{}
		return defaultSink
	}

	defaultSink = client
	return defaultSink
}

func SetDefaultForTesting(sink Sink) func() {
	defaultMu.Lock()
	prev := defaultSink
	if sink == nil {
		sink = noopSink{}
	}
	defaultSink = sink
	defaultMu.Unlock()

	return func() {
		defaultMu.Lock()
		defaultSink = prev
		defaultMu.Unlock()
	}
}

func Track(name string, fields Fields) {
	Default().Track(name, fields)
}

func Pageview(path string, fields Fields) {
	Default().Pageview(path, fields)
}

func Close(ctx context.Context) error {
	return Default().Close(ctx)
}

func (c *Client) Track(name string, fields Fields) {
	if c == nil || strings.TrimSpace(name) == "" {
		return
	}
	url := fmt.Sprintf("app://%s/%s", c.app, name)
	c.sendAsync(name, url, fields)
}

func (c *Client) Pageview(path string, fields Fields) {
	if c == nil {
		return
	}
	url := strings.TrimSpace(path)
	if url == "" {
		url = "/"
	}
	if !strings.HasPrefix(url, "/") {
		url = "/" + url
	}
	c.sendAsync("", url, fields)
}

func (c *Client) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}

	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.wg.Wait()
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (noopSink) Track(string, Fields) {}

func (noopSink) Pageview(string, Fields) {}

func (noopSink) Close(context.Context) error { return nil }

func (c *Client) sendAsync(name, eventURL string, fields Fields) {
	body, err := c.newRequest(name, eventURL, fields)
	if err != nil {
		return
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.wg.Add(1)
	c.mu.Unlock()

	go func(payload []byte) {
		defer c.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		c.send(ctx, payload)
	}(body)
}

func (c *Client) newRequest(name, eventURL string, fields Fields) ([]byte, error) {
	data := make(map[string]any, len(fields)+3)
	for k, v := range fields {
		data[k] = v
	}
	data["app_version"] = c.version
	data["goos"] = c.goos
	data["goarch"] = c.goarch

	return json.Marshal(collectRequest{
		Type: "event",
		Payload: collectPayload{
			Website:   c.websiteID,
			Hostname:  defaultHostname,
			Title:     defaultTitle,
			URL:       eventURL,
			Name:      name,
			Data:      data,
			Timestamp: time.Now().Unix(),
		},
	})
}

func (c *Client) send(ctx context.Context, payload []byte) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("ezoss/%s telemetry", c.version))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}

func normalizeEndpoint(host string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(host))
	if err != nil {
		return "", fmt.Errorf("parse host: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid host %q", host)
	}
	if strings.HasSuffix(u.Path, defaultPath) {
		return strings.TrimRight(u.String(), "/"), nil
	}
	base := strings.TrimRight(u.Path, "/")
	if base == "" {
		u.Path = defaultPath
	} else {
		u.Path = base + defaultPath
	}
	return u.String(), nil
}
