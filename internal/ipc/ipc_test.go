package ipc_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/kunchenguid/ezoss/internal/ipc"
)

func TestClientCallRoundTrip(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	srv := ipc.NewServer()
	srv.Handle(ipc.MethodHealth, func(_ context.Context, raw json.RawMessage) (interface{}, error) {
		var params ipc.HealthParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		return ipc.HealthResult{Status: "ok"}, nil
	})

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Serve(socketPath)
	}()
	t.Cleanup(func() {
		srv.Close()
		select {
		case err := <-serverErr:
			if err != nil {
				t.Errorf("Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("timed out waiting for server shutdown")
		}
	})

	waitForDial(t, socketPath)

	client, err := ipc.Dial(socketPath)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer client.Close()

	var result ipc.HealthResult
	if err := client.Call(ipc.MethodHealth, ipc.HealthParams{}, &result); err != nil {
		t.Fatalf("Call() error = %v", err)
	}

	if result.Status != "ok" {
		t.Fatalf("HealthResult.Status = %q, want %q", result.Status, "ok")
	}
}

func TestSubscribeStreamsEvents(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "daemon.sock")
	srv := ipc.NewServer()
	srv.HandleStream(ipc.MethodSubscribe, func(_ context.Context, raw json.RawMessage, send func(interface{}) error) error {
		var params ipc.SubscribeParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return err
		}
		status := "updated"
		return send(ipc.Event{Type: ipc.EventRecommendationUpdated, ItemID: params.ItemID, Status: &status})
	})

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Serve(socketPath)
	}()
	t.Cleanup(func() {
		srv.Close()
		select {
		case err := <-serverErr:
			if err != nil {
				t.Errorf("Serve() error = %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("timed out waiting for server shutdown")
		}
	})

	waitForDial(t, socketPath)

	events, cancel, err := ipc.Subscribe(socketPath, &ipc.SubscribeParams{ItemID: "owner/repo#42"})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	defer cancel()

	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("Subscribe() channel closed before first event")
		}
		if event.Type != ipc.EventRecommendationUpdated {
			t.Fatalf("Event.Type = %q, want %q", event.Type, ipc.EventRecommendationUpdated)
		}
		if event.ItemID != "owner/repo#42" {
			t.Fatalf("Event.ItemID = %q, want %q", event.ItemID, "owner/repo#42")
		}
		if event.Status == nil || *event.Status != "updated" {
			t.Fatalf("Event.Status = %v, want %q", event.Status, "updated")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscribe event")
	}
}

func waitForDial(t *testing.T, socketPath string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		client, err := ipc.Dial(socketPath)
		if err == nil {
			client.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %q did not become ready", socketPath)
}
