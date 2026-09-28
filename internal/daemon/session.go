package daemon

import (
	"context"
	"fmt"
	"time"
)

// NewSessionViewRuntime collects inside the dashboard process, never through
// the service manager. Other dashboards may share its socket; if its owner
// exits, a remaining dashboard takes over on its next reconnect.
func NewSessionViewRuntime(ctx context.Context, cfg Config) *ViewRuntime {
	r := NewViewRuntime(nil, cfg.SocketPath, cfg.Verbose)
	r.ensureClient = sessionConnector(ctx, cfg, startService)
	return r
}

func sessionConnector(lifetime context.Context, cfg Config, start func(context.Context, Config) (*Service, error)) func(context.Context) (*Client, error) {
	// ViewRuntime serializes calls with ensureMu.
	var owned *Service
	return func(ctx context.Context) (*Client, error) {
		if err := lifetime.Err(); err != nil {
			return nil, err
		}
		client := NewClient(cfg.SocketPath)
		if health, err := WaitForHealthInfo(ctx, client, 200*time.Millisecond); err == nil {
			if !HealthCurrent(health) {
				return nil, fmt.Errorf("running telemetry collector is incompatible; close other OpenUsage instances first")
			}
			return client, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if owned == nil {
			LoadServiceEnv()
			svc, err := start(lifetime, cfg)
			if err != nil {
				// Another dashboard may have won the socket startup race.
				if health, healthErr := WaitForHealthInfo(ctx, client, 200*time.Millisecond); healthErr == nil && HealthCurrent(health) {
					return client, nil
				}
				return nil, err
			}
			owned = svc
			go func() {
				<-lifetime.Done()
				_ = svc.Close()
			}()
		}
		if err := WaitForHealth(ctx, client, time.Second); err != nil {
			return nil, err
		}
		return client, nil
	}
}
