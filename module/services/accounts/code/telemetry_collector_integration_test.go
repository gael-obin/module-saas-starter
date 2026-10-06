//go:build otel_integration

package main

import (
	"context"
	"os"
	"testing"
	"time"
)

// The qualification runner supplies the official collector, not a test receiver.
func TestMetricsToInfrastructureCollector(t *testing.T) {
	endpoint := os.Getenv("QUALIFICATION_OTLP_ENDPOINT")
	if endpoint == "" {
		t.Fatal("run qualification/telemetry/run.py with an official collector binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	metrics, err := enableOTELMetrics(ctx, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := metrics.Shutdown(shutdown); err != nil {
			t.Error(err)
		}
	})
	counter, err := metrics.provider.Meter("qualification").Int64Counter("qualification.request.count")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(ctx, 1)
	if err := metrics.provider.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
}
