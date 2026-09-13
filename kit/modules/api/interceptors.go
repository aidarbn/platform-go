package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"buf.build/go/protovalidate"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	panics   prometheus.Counter
	unary    grpc.UnaryServerInterceptor
	stream   grpc.StreamServerInterceptor
}

func newMetrics(reg *prometheus.Registry) (*metrics, error) {
	m := &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "api", Name: "grpc_requests_total",
			Help: "gRPC calls, REST calls through the gateway included",
		}, []string{"method", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "api", Name: "grpc_request_duration_seconds",
			Help:    "time to handle a gRPC call",
			Buckets: prometheus.DefBuckets,
		}, []string{"method"}),
		panics: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "api", Name: "panics_total",
			Help: "panics recovered while handling a call",
		}),
	}
	for _, c := range []prometheus.Collector{m.requests, m.duration, m.panics} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("api metrics: %w", err)
		}
	}

	m.unary = func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		m.observe(info.FullMethod, err, start)
		return resp, err
	}
	m.stream = func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		m.observe(info.FullMethod, err, start)
		return err
	}
	return m, nil
}

func (m *metrics) observe(method string, err error, start time.Time) {
	m.requests.WithLabelValues(method, status.Code(err).String()).Inc()
	m.duration.WithLabelValues(method).Observe(time.Since(start).Seconds())
}

// recoverUnary turns a panic in a handler into an Internal error: one broken request
// must not take the service down.
func recoverUnary(log *slog.Logger, m *metrics) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if p := recover(); p != nil {
				m.panics.Inc()
				log.ErrorContext(ctx, "panic while handling a call", "method", info.FullMethod, "panic", p, "stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

func recoverStream(log *slog.Logger, m *metrics) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if p := recover(); p != nil {
				m.panics.Inc()
				log.Error("panic while handling a stream", "method", info.FullMethod, "panic", p, "stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(srv, ss)
	}
}

func recoverHTTP(log *slog.Logger, m *metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				m.panics.Inc()
				log.ErrorContext(r.Context(), "panic while handling an HTTP request", "path", r.URL.Path, "panic", p, "stack", string(debug.Stack()))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// logUnary logs failed calls; successful ones only at debug level, so the log is about
// what needs attention.
func logUnary(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logCall(ctx, log, info.FullMethod, err, time.Since(start))
		return resp, err
	}
}

func logStream(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		logCall(ss.Context(), log, info.FullMethod, err, time.Since(start))
		return err
	}
}

func logCall(ctx context.Context, log *slog.Logger, method string, err error, took time.Duration) {
	code := status.Code(err)
	attrs := []any{"method", method, "code", code.String(), "duration", took}
	switch code {
	case codes.OK:
		log.DebugContext(ctx, "call handled", attrs...)
	case codes.Internal, codes.Unknown, codes.DataLoss, codes.Unavailable:
		log.ErrorContext(ctx, "call failed", append(attrs, "err", err)...)
	default:
		log.InfoContext(ctx, "call rejected", append(attrs, "err", err)...)
	}
}

// errorsUnary keeps internal details out of responses: an error that is not a gRPC
// status is logged in full and answered with a plain Internal.
func errorsUnary(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err == nil {
			return resp, nil
		}
		if _, ok := status.FromError(err); ok {
			return resp, err
		}
		switch {
		case errors.Is(err, context.Canceled):
			return resp, status.Error(codes.Canceled, "the call was canceled")
		case errors.Is(err, context.DeadlineExceeded):
			return resp, status.Error(codes.DeadlineExceeded, "the call took too long")
		}
		log.ErrorContext(ctx, "handler returned an error without a status", "method", info.FullMethod, "err", err)
		return resp, status.Error(codes.Internal, "internal error")
	}
}

// validateUnary checks the request against the protovalidate rules in the proto file,
// so a handler only ever sees a valid request. The violations go back as details.
func validateUnary(v protovalidate.Validator) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		msg, ok := req.(proto.Message)
		if !ok {
			return handler(ctx, req)
		}
		if err := v.Validate(msg); err != nil {
			var verr *protovalidate.ValidationError
			if !errors.As(err, &verr) {
				return nil, status.Error(codes.Internal, "request validation failed")
			}
			st := status.New(codes.InvalidArgument, verr.Error())
			if detailed, derr := st.WithDetails(verr.ToProto()); derr == nil {
				st = detailed
			}
			return nil, st.Err()
		}
		return handler(ctx, req)
	}
}
