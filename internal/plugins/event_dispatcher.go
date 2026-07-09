package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

type eventConsumerClient interface {
	HandleEvent(ctx context.Context, req *pluginv1.HandleEventRequest) (*pluginv1.HandleEventResponse, error)
}

type eventConsumerResolver interface {
	EventConsumerClient(ctx context.Context, installationID int, capabilityID string) (eventConsumerClient, error)
}

// EventDispatcher delivers host events to plugin event_consumer.v1 capabilities
// whose manifest "subscriptions" list matches the event name. It listens on
// two sources:
//
//   - The cache.EventBus channels (catalog/admin/playback) for events that
//     direct callers — host code in api/handlers and worker packages —
//     publish via the bus.
//   - The events.Hub for envelopes routed through the in-process hub. This is
//     the path plugin-published events take (RuntimeHostServer.PublishEvent
//     stamps `plugin.<id>.` and writes an envelope on ChannelPlugins). Without
//     this subscription, plugin↔plugin events never reach consumers.
//
// Dispatch is fan-out only: every subscriber to event_name receives the event.
// Plugins wanting to address a single peer should embed a target identifier
// in the payload and have subscribers filter on it.
type EventDispatcher struct {
	bus           cache.EventBus
	hub           *events.Hub
	installations taskInstallationStore
	resolver      eventConsumerResolver
	concurrency   int
	sem           chan struct{}

	hubUnsubscribe func()
}

func NewEventDispatcher(
	bus cache.EventBus,
	hub *events.Hub,
	installations taskInstallationStore,
	resolver eventConsumerResolver,
	concurrency int,
) *EventDispatcher {
	if concurrency < 1 {
		concurrency = 4
	}
	return &EventDispatcher{
		bus:           bus,
		hub:           hub,
		installations: installations,
		resolver:      resolver,
		concurrency:   concurrency,
		sem:           make(chan struct{}, concurrency),
	}
}

func NewEventDispatcherWithTypedResolver(
	bus cache.EventBus,
	hub *events.Hub,
	installations taskInstallationStore,
	resolver interface {
		EventConsumerClient(ctx context.Context, installationID int, capabilityID string) (*pluginhost.EventConsumerClient, error)
	},
	concurrency int,
) *EventDispatcher {
	return NewEventDispatcher(bus, hub, installations, eventConsumerResolverFunc(func(
		ctx context.Context,
		installationID int,
		capabilityID string,
	) (eventConsumerClient, error) {
		return resolver.EventConsumerClient(ctx, installationID, capabilityID)
	}), concurrency)
}

func (d *EventDispatcher) Start(ctx context.Context) error {
	for _, channel := range []string{cache.ChannelCatalog, cache.ChannelAdmin, cache.ChannelPlayback} {
		channel := channel
		if err := d.bus.Subscribe(ctx, channel, func(event cache.Event) {
			d.dispatchBusEvent(ctx, event)
		}); err != nil {
			return fmt.Errorf("subscribe plugin event dispatcher to %s: %w", channel, err)
		}
	}

	if d.hub != nil {
		envCh, unsub := d.hub.Subscribe()
		d.hubUnsubscribe = unsub
		go d.consumeHub(ctx, envCh)
	}
	return nil
}

func (d *EventDispatcher) Stop() {
	if d.hubUnsubscribe != nil {
		d.hubUnsubscribe()
		d.hubUnsubscribe = nil
	}
}

func (d *EventDispatcher) consumeHub(ctx context.Context, ch <-chan events.Envelope) {
	for {
		select {
		case <-ctx.Done():
			return
		case env, ok := <-ch:
			if !ok {
				return
			}
			d.dispatchEnvelope(ctx, env)
		}
	}
}

func (d *EventDispatcher) dispatchBusEvent(ctx context.Context, event cache.Event) {
	d.fanOut(ctx, events.Envelope{Event: event.Type}, decodeStringPayload(event.Payload))
}

func (d *EventDispatcher) dispatchEnvelope(ctx context.Context, env events.Envelope) {
	d.fanOut(ctx, env, decodeRawJSON(env.Data))
}

// decodeStringPayload tries to JSON-decode a cache.Event.Payload string into a
// structpb. Plugin consumers expect top-level fields (e.g. p["libraryId"]), so
// when the payload parses we pass it through as-is. If it doesn't parse — the
// payload is an opaque id rather than JSON — we still hand the raw value over
// under "raw" so the consumer can ignore it without misreading nil fields.
func decodeStringPayload(raw string) *structpb.Struct {
	if raw == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err == nil {
		s, _ := structpb.NewStruct(m)
		return s
	}
	s, _ := structpb.NewStruct(map[string]any{"raw": raw})
	return s
}

func decodeRawJSON(raw json.RawMessage) *structpb.Struct {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	s, _ := structpb.NewStruct(m)
	return s
}

// fanOut dispatches env to every plugin event_consumer.v1 capability that
// declares an exact-name subscription matching env.Event.
func (d *EventDispatcher) fanOut(ctx context.Context, env events.Envelope, payload *structpb.Struct) {
	installations, err := d.installations.ListEnabled(ctx)
	if err != nil {
		slog.WarnContext(ctx, "plugin event dispatcher: list installations failed", "component", "plugins", "error", err)
		return
	}

	var wg sync.WaitGroup
	for _, installation := range installations {
		if env.TargetPluginID != "" && installation.PluginID != env.TargetPluginID {
			continue
		}

		capabilities, err := d.installations.ListCapabilities(ctx, installation.ID)
		if err != nil {
			slog.WarnContext(ctx, "plugin event dispatcher: list capabilities failed", "component", "plugins", "installation_id", installation.ID, "error", err)
			continue
		}

		for _, capability := range capabilities {
			if capability == nil || capability.Type != "event_consumer.v1" {
				continue
			}
			if !subscribesTo(capability, env.Event) {
				continue
			}

			wg.Add(1)
			d.sem <- struct{}{}
			go func(installationID int, capabilityID string) {
				defer wg.Done()
				defer func() { <-d.sem }()

				client, err := d.resolver.EventConsumerClient(ctx, installationID, capabilityID)
				if err != nil {
					slog.WarnContext(ctx, "plugin event dispatcher: resolve client failed", "component", "plugins", "installation_id", installationID, "capability_id", capabilityID, "error", err)
					return
				}

				if _, err := client.HandleEvent(ctx, &pluginv1.HandleEventRequest{
					EventName: env.Event,
					Payload:   payload,
				}); err != nil {
					slog.WarnContext(ctx, "plugin event dispatcher: delivery failed", "component", "plugins", "installation_id", installationID, "capability_id", capabilityID, "error", err)
				}
			}(installation.ID, capability.ID)
			// Deliver to the first matching event_consumer.v1 capability per
			// installation to preserve one-delivery-per-install semantics.
			break
		}
	}
	wg.Wait()
}

func subscribesTo(capability *Capability, eventType string) bool {
	if capability == nil {
		return false
	}
	subscriptions, ok := capability.Metadata["subscriptions"]
	if !ok {
		return false
	}
	values, err := toStringSlice(subscriptions)
	if err != nil {
		return false
	}
	for _, subscription := range values {
		if subscription == eventType {
			return true
		}
	}
	return false
}

type eventConsumerResolverFunc func(ctx context.Context, installationID int, capabilityID string) (eventConsumerClient, error)

func (f eventConsumerResolverFunc) EventConsumerClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (eventConsumerClient, error) {
	return f(ctx, installationID, capabilityID)
}
