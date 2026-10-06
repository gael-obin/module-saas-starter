//go:build otel_integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/codefly-dev/core/sdk/dependencies"
	"github.com/codefly-dev/core/standards"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/stretchr/testify/require"
)

// This executes the application's actual main in a separate process. Only the
// test launcher is conditional; startup, handlers and shutdown are production code.
func TestFullServiceToInfrastructureCollector(t *testing.T) {
	if os.Getenv("QUALIFICATION_GATEWAY_PROCESS") == "1" {
		main()
		return
	}
	endpoint := os.Getenv("QUALIFICATION_OTLP_ENDPOINT")
	require.NotEmpty(t, endpoint, "run qualification/telemetry/run.py --full-gateway")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	session, err := dependencies.WithDependencies(ctx,
		dependencies.WithFixture("dev-admin"),
		dependencies.WithWorkspaceConfiguration("observability", "OBSERVABILITY_STATE", "enabled"),
		dependencies.WithWorkspaceConfiguration("observability", "OTEL_EXPORTER_OTLP_ENDPOINT", endpoint),
		dependencies.WithWorkspaceConfiguration("observability", "OTEL_EXPORTER_OTLP_PROTOCOL", "grpc"),
		dependencies.WithWorkspaceConfiguration("observability", "OBSERVABILITY_COLLECTOR_TIER", "node-agent"),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		require.NoError(t, session.Destroy(cleanup))
	})
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", os.Getenv("OTEL_RESOURCE_ATTRIBUTES")+",qualification.phase=full-service")
	_, err = codefly.Init(ctx)
	require.NoError(t, err)
	httpEndpoint, err := codefly.For(ctx).API(standards.REST).ResolveNetworkInstance()
	require.NoError(t, err)

	executable, err := os.Executable()
	require.NoError(t, err)
	log, err := os.Create(filepath.Join(t.TempDir(), "gateway.log"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = log.Close() })
	child := exec.CommandContext(ctx, executable, "-test.run=^TestFullServiceToInfrastructureCollector$", "-test.count=1")
	child.Env = append(os.Environ(), "QUALIFICATION_GATEWAY_PROCESS=1")
	child.Stdout, child.Stderr = log, log
	require.NoError(t, child.Start())
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = child.Process.Kill()
			<-done
		}
		if t.Failed() {
			body, readErr := os.ReadFile(log.Name())
			if readErr == nil {
				t.Log(string(body))
			}
		}
	})

	client := &http.Client{Timeout: time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/ready", httpEndpoint.Port)
	deadline := time.Now().Add(40 * time.Second)
	var health struct {
		Status string `json:"status"`
	}
	for {
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("Gateway exited before readiness: %v", err)
		default:
		}
		response, requestErr := client.Get(url)
		if requestErr == nil {
			decodeErr := json.NewDecoder(response.Body).Decode(&health)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && health.Status == "ok" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("Gateway did not report healthy dependencies")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// Graceful shutdown flushes both real telemetry providers to the collector.
	require.NoError(t, child.Process.Signal(os.Interrupt))
	select {
	case err := <-done:
		stopped = true
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("Gateway did not shut down and flush its providers")
	}
}
