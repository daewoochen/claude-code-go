package runtime

import (
	"context"
	"time"
)

type EventSink func(Event)

type eventSinkKey struct{}

func WithEventSink(ctx context.Context, sink EventSink) context.Context {
	return context.WithValue(ctx, eventSinkKey{}, sink)
}

func Emit(ctx context.Context, event Event) {
	sink, ok := ctx.Value(eventSinkKey{}).(EventSink)
	if !ok || sink == nil {
		return
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	sink(event)
}
