package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionCollectorLifetimeAndTakeover(t *testing.T) {
	cfg := Config{SocketPath: filepath.Join(t.TempDir(), "daemon.sock")}
	ownerCtx, stopOwner := context.WithCancel(context.Background())
	defer stopOwner()
	otherCtx, stopOther := context.WithCancel(context.Background())
	defer stopOther()
	starts := 0
	// Exercise real socket lifecycle without collecting personal provider data.
	start := func(ctx context.Context, cfg Config) (*Service, error) {
		starts++
		svc := &Service{cfg: cfg}
		return svc, svc.startSocketServer(ctx)
	}
	owner := sessionConnector(ownerCtx, cfg, start)
	other := sessionConnector(otherCtx, cfg, start)
	requestCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := owner(requestCtx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other(requestCtx); err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatalf("shared collector started %d times", starts)
	}
	stopOwner()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := WaitForHealthInfo(requestCtx, client, 10*time.Millisecond); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("collector survived dashboard cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := owner(requestCtx); err != context.Canceled {
		t.Fatalf("closed dashboard reconnect: %v", err)
	}
	if _, err := other(requestCtx); err != nil {
		t.Fatal(err)
	}
	if starts != 2 {
		t.Fatalf("remaining dashboard did not take over: %d starts", starts)
	}
}

func TestCanceledSessionDoesNotStartCollector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	connect := sessionConnector(ctx, Config{}, func(context.Context, Config) (*Service, error) {
		t.Fatal("started collector after dashboard exit")
		return nil, nil
	})
	if _, err := connect(context.Background()); err != context.Canceled {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
