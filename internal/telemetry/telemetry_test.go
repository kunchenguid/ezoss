package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kunchenguid/ezoss/internal/buildinfo"
)

func TestDefaultReturnsNoopWhenWebsiteIDMissing(t *testing.T) {
	t.Setenv(umamiWebsiteIDEnv, "")
	prevBuildID := buildinfo.TelemetryWebsiteID
	buildinfo.TelemetryWebsiteID = ""
	t.Cleanup(func() { buildinfo.TelemetryWebsiteID = prevBuildID })
	reset := SetDefaultForTesting(nil)
	defer reset()

	if _, ok := Default().(noopSink); !ok {
		t.Fatalf("Default() type = %T, want noopSink", Default())
	}
}

// resetDefaultSink clears the cached package-level Sink so the next
// Default() call re-runs its env/buildinfo precedence. Required because
// SetDefaultForTesting(nil) installs a noopSink, not a nil sink.
func resetDefaultSink(t *testing.T) {
	t.Helper()
	defaultMu.Lock()
	prev := defaultSink
	defaultSink = nil
	defaultMu.Unlock()
	t.Cleanup(func() {
		defaultMu.Lock()
		defaultSink = prev
		defaultMu.Unlock()
	})
}

func TestDefaultUsesBuildTimeWebsiteIDWhenEnvUnset(t *testing.T) {
	t.Setenv(umamiWebsiteIDEnv, "")
	prevBuildID := buildinfo.TelemetryWebsiteID
	buildinfo.TelemetryWebsiteID = "build-injected-id"
	t.Cleanup(func() { buildinfo.TelemetryWebsiteID = prevBuildID })
	resetDefaultSink(t)

	sink := Default()
	client, ok := sink.(*Client)
	if !ok {
		t.Fatalf("Default() type = %T, want *Client", sink)
	}
	if client.websiteID != "build-injected-id" {
		t.Fatalf("websiteID = %q, want %q", client.websiteID, "build-injected-id")
	}
}

func TestDefaultEnvVarOverridesBuildTimeWebsiteID(t *testing.T) {
	t.Setenv(umamiWebsiteIDEnv, "env-override-id")
	prevBuildID := buildinfo.TelemetryWebsiteID
	buildinfo.TelemetryWebsiteID = "build-injected-id"
	t.Cleanup(func() { buildinfo.TelemetryWebsiteID = prevBuildID })
	resetDefaultSink(t)

	sink := Default()
	client, ok := sink.(*Client)
	if !ok {
		t.Fatalf("Default() type = %T, want *Client", sink)
	}
	if client.websiteID != "env-override-id" {
		t.Fatalf("websiteID = %q, want env override %q", client.websiteID, "env-override-id")
	}
}

func TestClientTrackSendsUmamiEventPayload(t *testing.T) {
	t.Parallel()

	type requestBody struct {
		Type    string `json:"type"`
		Payload struct {
			Website string         `json:"website"`
			Name    string         `json:"name"`
			URL     string         `json:"url"`
			Title   string         `json:"title"`
			Data    map[string]any `json:"data"`
		} `json:"payload"`
	}

	reqCh := make(chan requestBody, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/send" {
			t.Fatalf("path = %s, want /api/send", r.URL.Path)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		defer r.Body.Close()

		var got requestBody
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("unmarshal body: %v\nbody=%s", err, string(body))
		}
		reqCh <- got

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"disabled":false}`)
	}))
	defer server.Close()

	client, err := NewClient(Config{
		Host:       server.URL,
		WebsiteID:  "website-123",
		App:        "ezoss",
		Version:    "v1.2.3",
		GOOS:       "darwin",
		GOARCH:     "arm64",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	client.Track("command", Fields{
		"command": "init",
		"status":  "success",
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	select {
	case got := <-reqCh:
		if got.Type != "event" {
			t.Fatalf("type = %q, want %q", got.Type, "event")
		}
		if got.Payload.Website != "website-123" {
			t.Fatalf("website = %q, want %q", got.Payload.Website, "website-123")
		}
		if got.Payload.Name != "command" {
			t.Fatalf("name = %q, want %q", got.Payload.Name, "command")
		}
		if got.Payload.URL != "app://ezoss/command" {
			t.Fatalf("url = %q, want %q", got.Payload.URL, "app://ezoss/command")
		}
		if got.Payload.Title != "ezoss CLI" {
			t.Fatalf("title = %q, want %q", got.Payload.Title, "ezoss CLI")
		}
		if got.Payload.Data["command"] != "init" {
			t.Fatalf("data.command = %v, want %q", got.Payload.Data["command"], "init")
		}
		if got.Payload.Data["status"] != "success" {
			t.Fatalf("data.status = %v, want %q", got.Payload.Data["status"], "success")
		}
		if got.Payload.Data["app_version"] != "v1.2.3" {
			t.Fatalf("data.app_version = %v, want %q", got.Payload.Data["app_version"], "v1.2.3")
		}
		if got.Payload.Data["goos"] != "darwin" {
			t.Fatalf("data.goos = %v, want %q", got.Payload.Data["goos"], "darwin")
		}
		if got.Payload.Data["goarch"] != "arm64" {
			t.Fatalf("data.goarch = %v, want %q", got.Payload.Data["goarch"], "arm64")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for telemetry request")
	}
}
