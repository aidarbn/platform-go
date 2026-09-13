package api

import (
	"context"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// deprecatedMethods reads `option deprecated = true` of the registered methods from their
// proto descriptors. taply keeps this list by hand; here the proto file is the only place.
func deprecatedMethods(srv *grpc.Server) map[string]bool {
	out := map[string]bool{}
	for service := range srv.GetServiceInfo() {
		desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service))
		if err != nil {
			continue
		}
		sd, ok := desc.(protoreflect.ServiceDescriptor)
		if !ok {
			continue
		}
		methods := sd.Methods()
		for i := range methods.Len() {
			m := methods.Get(i)
			if opts, ok := m.Options().(*descriptorpb.MethodOptions); ok && opts.GetDeprecated() {
				out["/"+service+"/"+string(m.Name())] = true
			}
		}
	}
	return out
}

// deprecatedUnary counts and logs calls to deprecated methods, so a method can be removed
// once nobody calls it.
func deprecatedUnary(methods map[string]bool, log *slog.Logger, calls *prometheus.CounterVec) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if methods[info.FullMethod] {
			calls.WithLabelValues(info.FullMethod).Inc()
			log.WarnContext(ctx, "deprecated method called", "method", info.FullMethod,
				"ip", ClientIP(ctx), "user_agent", userAgent(ctx))
		}
		return handler(ctx, req)
	}
}
