package pluginhost

import (
	"context"
	"net/http"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// Only methods shipped by the pinned SDK can become a label. Installation IDs,
// plugin IDs, capability IDs and request contents are deliberately absent.
var pluginOperations = func() map[string]string {
	ops := map[string]string{"/grpc.health.v1.Health/Check": "Health.Check"}
	for _, service := range []*grpc.ServiceDesc{
		&pluginv1.Runtime_ServiceDesc, &pluginv1.RuntimeHost_ServiceDesc,
		&pluginv1.MetadataProvider_ServiceDesc, &pluginv1.ImageResolver_ServiceDesc,
		&pluginv1.MarkerProvider_ServiceDesc, &pluginv1.MediaAnalyzer_ServiceDesc,
		&pluginv1.ScheduledTask_ServiceDesc, &pluginv1.ScanSource_ServiceDesc,
		&pluginv1.RequestRouter_ServiceDesc, &pluginv1.EventConsumer_ServiceDesc,
		&pluginv1.AuthProvider_ServiceDesc, &pluginv1.HttpRoutes_ServiceDesc,
		&pluginv1.WatchSyncProvider_ServiceDesc, &pluginv1.WatchSyncDeviceAuthorizationService_ServiceDesc,
	} {
		for _, method := range service.Methods {
			ops["/"+service.ServiceName+"/"+method.MethodName] = strings.TrimPrefix(service.ServiceName, "silo.plugin.v1.") + "." + method.MethodName
		}
	}
	return ops
}()

func pluginOperation(method string) string {
	if operation := pluginOperations[method]; operation != "" {
		return operation
	}
	return "other"
}

// The local process transport is known at construction; trace context can be
// forwarded to a future SDK consumer without exposing caller baggage. Current
// SDK plugins do not extract it, so spans cover the host RPC duration only.
func observePluginRPC(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	ctx, finish := telemetry.StartDependency(ctx, "plugin", "worker", pluginOperation(method))
	headers := make(http.Header)
	telemetry.InjectTrusted(ctx, headers)
	md, _ := metadata.FromOutgoingContext(ctx)
	md = md.Copy()
	md.Delete("baggage")
	md.Delete("traceparent")
	md.Delete("tracestate")
	for name, values := range headers {
		md.Set(strings.ToLower(name), values...)
	}
	err := invoker(metadata.NewOutgoingContext(ctx, md), method, req, reply, cc, opts...)
	finish(err)
	return err
}

// Plugin callbacks receive their own sampling decision. A plugin cannot force
// server-wide sampling by presenting a sampled parent or arbitrary baggage.
func observePluginCallback(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	ctx, span := otel.Tracer("silo/plugins").Start(telemetry.PublicContext(ctx), "plugin."+pluginOperation(info.FullMethod), trace.WithSpanKind(trace.SpanKindServer), trace.WithNewRoot())
	defer span.End()
	out, err := handler(ctx, req)
	if err != nil {
		span.SetStatus(codes.Error, telemetry.Outcome(err))
	}
	return out, err
}
