package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/instrument"
	metricsdk "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/aggregation"
	"go.opentelemetry.io/otel/sdk/metric/view"
)

type Gauge struct{ value int64 }

func (g *Gauge) Record(_ context.Context, v int64) {
	atomic.StoreInt64(&g.value, v)
}

func MakeSyncGauge(meter metric.Meter, name string) (*Gauge, error) {
	asyncInst, err := meter.AsyncInt64().Gauge(name)
	if err != nil {
		return nil, err
	}
	g := &Gauge{}
	meter.RegisterCallback([]instrument.Asynchronous{asyncInst}, func(ctx context.Context) {
		asyncInst.Observe(ctx, atomic.LoadInt64(&g.value))
	})
	return g, nil
}

func main() {
	ctx := context.Background()

	// attempts to override the aggregation policy for maybe_a_gauge. doesn't
	// work because aggregation.LastValue is not an allowed aggregation type for
	// SyncUpdDownCounter instruments (hard-coded in otel pipeline.go)
	gaugeView, err := view.New(
		view.MatchInstrumentName("maybe_a_gauge_xxx"), // remove _xxx to see this (fail) in action
		view.MatchInstrumentKind(view.SyncUpDownCounter),
		view.WithSetAggregation(aggregation.LastValue{}),
	)
	if err != nil {
		log.Fatal(err)
	}

	defaultView, err := view.New(
		view.MatchInstrumentName("*"),
	)
	if err != nil {
		log.Fatal(err)
	}

	exporter := otelprom.New()
	provider := metricsdk.NewMeterProvider(metricsdk.WithReader(exporter, gaugeView, defaultView))
	meter := provider.Meter("github.com/mmcshane/otel-metric-test")

	go serveMetrics(exporter.Collector)

	notAGauge, err := meter.SyncInt64().UpDownCounter("not_a_gauge", instrument.WithDescription("a fun little non-gauge"))
	if err != nil {
		log.Fatal(err)
	}
	notAGauge.Add(ctx, 100)
	notAGauge.Add(ctx, -25)

	maybeAGauge, err := meter.SyncInt64().UpDownCounter("maybe_a_gauge", instrument.WithDescription("maybe a gauge"))
	if err != nil {
		log.Fatal(err)
	}
	maybeAGauge.Add(ctx, 100)
	maybeAGauge.Add(ctx, -25)

	probablyAGauge, err := MakeSyncGauge(meter, "probably_a_gauge")
	if err != nil {
		log.Fatal(err)
	}
	probablyAGauge.Record(ctx, 100)
	probablyAGauge.Record(ctx, -25)

	ctx, _ = signal.NotifyContext(ctx, os.Interrupt)
	<-ctx.Done()
}

func serveMetrics(collector prometheus.Collector) {
	registry := prometheus.NewRegistry()
	err := registry.Register(collector)
	if err != nil {
		fmt.Printf("error registering collector: %v", err)
		return
	}

	log.Printf("serving metrics at localhost:2222/metrics")
	http.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	err = http.ListenAndServe(":2222", nil)
	if err != nil {
		fmt.Printf("error serving http: %v", err)
		return
	}
}
