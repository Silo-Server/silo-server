package handlers

import (
	"context"
)

type artworkProbeFunc func(context.Context) error

func (f artworkProbeFunc) Probe(ctx context.Context) error { return f(ctx) }

type pingerFunc func(context.Context) error

func (f pingerFunc) Ping(ctx context.Context) error { return f(ctx) }
