package gateway

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type fakeStageHandler struct {
	name   string
	stages []StageName
	result StageResult
}

func (h *fakeStageHandler) Name() string          { return h.name }
func (h *fakeStageHandler) Stages() []StageName   { return h.stages }
func (h *fakeStageHandler) Handle(*GatewayContext) StageResult {
	return h.result
}

// TestRunStage_EmitsCentralEvent asserts that every handler dispatched via
// RunStage produces a `gateway.stage` event on the active span, carrying
// stage/handler/action attributes. The central dispatcher is the only place
// that adds these events — verifying once here covers all 13 production
// handlers without per-handler boilerplate.
func TestRunStage_EmitsCentralEvent(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	ctx, span := tp.Tracer("test").Start(context.Background(), "root")
	gw := &GatewayContext{Context: ctx}

	h1 := &fakeStageHandler{
		name:   "h1",
		stages: []StageName{StageBeforeUpstream},
		result: StageResult{Action: ActionContinue},
	}
	h2 := &fakeStageHandler{
		name:   "h2",
		stages: []StageName{StageBeforeUpstream},
		result: StageResult{Action: ActionReject, StatusCode: 401},
	}

	p := NewPipeline(h1, h2)
	res, halt := p.RunStage(StageBeforeUpstream, gw)
	if !halt || res.Action != ActionReject {
		t.Fatalf("RunStage: got (%v, halt=%v), want (reject, true)", res, halt)
	}
	span.End()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("want 1 ended span, got %d", len(spans))
	}
	events := spans[0].Events()
	if len(events) != 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}

	for i, want := range []struct {
		handler, action string
	}{
		{"h1", "continue"},
		{"h2", "reject"},
	} {
		if events[i].Name != "gateway.stage" {
			t.Errorf("event[%d].Name=%q", i, events[i].Name)
		}
		var gotHandler, gotAction, gotStage string
		var sawLatency bool
		for _, a := range events[i].Attributes {
			switch a.Key {
			case "handler":
				gotHandler = a.Value.AsString()
			case "action":
				gotAction = a.Value.AsString()
			case "stage":
				gotStage = a.Value.AsString()
			case "latency_ms":
				sawLatency = true
			}
		}
		if gotHandler != want.handler {
			t.Errorf("event[%d] handler=%q want %q", i, gotHandler, want.handler)
		}
		if gotAction != want.action {
			t.Errorf("event[%d] action=%q want %q", i, gotAction, want.action)
		}
		if gotStage != string(StageBeforeUpstream) {
			t.Errorf("event[%d] stage=%q want %q", i, gotStage, StageBeforeUpstream)
		}
		if !sawLatency {
			t.Errorf("event[%d] missing latency_ms attr", i)
		}
	}
}
