//go:build otel_integration

package main

import (
	"accounts/pkg/adapters"
	gen "accounts/pkg/gen/saas/accounts/v1"
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
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// This executes the application's actual main in a separate process. Only the
// test launcher is conditional; startup, handlers and shutdown are production code.
func TestFullServiceToInfrastructureCollector(t *testing.T) {
	if os.Getenv("QUALIFICATION_ACCOUNTS_PROCESS") == "1" {
		main()
		return
	}
	endpoint := os.Getenv("QUALIFICATION_OTLP_ENDPOINT")
	require.NotEmpty(t, endpoint, "run qualification/telemetry/run.py --full-accounts")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", os.Getenv("OTEL_RESOURCE_ATTRIBUTES")+",qualification.phase=full-service")
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
	_, err = codefly.Init(ctx)
	require.NoError(t, err)
	httpEndpoint, err := codefly.For(ctx).API(standards.REST).ResolveNetworkInstance()
	require.NoError(t, err)
	grpcEndpoint, err := codefly.For(ctx).API(standards.GRPC).ResolveNetworkInstance()
	require.NoError(t, err)

	executable, err := os.Executable()
	require.NoError(t, err)
	log, err := os.Create(filepath.Join(t.TempDir(), "accounts.log"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = log.Close() })
	child := exec.CommandContext(ctx, executable, "-test.run=^TestFullServiceToInfrastructureCollector$", "-test.count=1")
	child.Env = append(os.Environ(), "QUALIFICATION_ACCOUNTS_PROCESS=1")
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
	url := fmt.Sprintf("http://127.0.0.1:%d/v1/status", httpEndpoint.Port)
	deadline := time.Now().Add(40 * time.Second)
	var health adapters.StatusResponse
	for {
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("Accounts exited before readiness: %v", err)
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
			t.Fatal("Accounts did not report healthy dependencies")
		}
		time.Sleep(100 * time.Millisecond)
	}
	components := make(map[string]string)
	for _, component := range health.Components {
		components[component.Name] = component.Status
	}
	require.Equal(t, map[string]string{"membership-cache": "ok", "postgres": "ok", "vault": "ok"}, components)
	connection, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", grpcEndpoint.Port), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer connection.Close()
	_, err = gen.NewUserServiceClient(connection).Version(ctx, &gen.VersionRequest{})
	require.NoError(t, err)
	// Graceful shutdown flushes both real telemetry providers to the collector.
	require.NoError(t, child.Process.Signal(os.Interrupt))
	select {
	case err := <-done:
		stopped = true
		require.NoError(t, err)
	case <-time.After(15 * time.Second):
		t.Fatal("Accounts did not shut down and flush its providers")
	}
}
