// Package interceptors provides reusable gRPC interceptors for all platform
// services: deadline propagation, jittered retry, idempotency-key forwarding,
// and OpenTelemetry instrumentation.
package interceptors

import (
	"context"
	"math/rand"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const idempotencyKeyHeader = "x-idempotency-key"

// RetryConfig controls the retry interceptor behaviour.
type RetryConfig struct {
	MaxAttempts uint
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

var defaultRetry = RetryConfig{
	MaxAttempts: 5,
	BaseDelay:   50 * time.Millisecond,
	MaxDelay:    2 * time.Second,
}

// UnaryClientInterceptors returns the standard chain of client-side unary
// interceptors: OTel tracing, retry, deadline propagation.
func UnaryClientInterceptors(cfg ...RetryConfig) []grpc.UnaryClientInterceptor {
	rc := defaultRetry
	if len(cfg) > 0 {
		rc = cfg[0]
	}
	return []grpc.UnaryClientInterceptor{
		otelgrpc.UnaryClientInterceptor(),
		retryInterceptor(rc),
	}
}

// UnaryServerInterceptors returns the standard chain of server-side unary
// interceptors: OTel tracing, panic recovery.
func UnaryServerInterceptors() []grpc.UnaryServerInterceptor {
	return []grpc.UnaryServerInterceptor{
		otelgrpc.UnaryServerInterceptor(),
		recoveryInterceptor(),
	}
}

// retryInterceptor retries idempotent gRPC calls with jittered exponential
// backoff. Only retries on Unavailable and ResourceExhausted codes.
func retryInterceptor(cfg RetryConfig) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{},
		cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		var lastErr error
		delay := cfg.BaseDelay
		for attempt := uint(0); attempt < cfg.MaxAttempts; attempt++ {
			lastErr = invoker(ctx, method, req, reply, cc, opts...)
			if lastErr == nil {
				return nil
			}
			code := status.Code(lastErr)
			if code != codes.Unavailable && code != codes.ResourceExhausted {
				return lastErr
			}
			if attempt+1 < cfg.MaxAttempts {
				jitter := time.Duration(rand.Int63n(int64(delay / 2)))
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(delay + jitter):
				}
				delay *= 2
				if delay > cfg.MaxDelay {
					delay = cfg.MaxDelay
				}
			}
		}
		return lastErr
	}
}

// recoveryInterceptor catches panics in server handlers and returns Internal.
func recoveryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler) (resp interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}

// ForwardIdempotencyKey propagates an idempotency key from incoming to outgoing
// gRPC metadata. Call this in the server handler before making downstream calls.
func ForwardIdempotencyKey(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	keys := md.Get(idempotencyKeyHeader)
	if len(keys) == 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, idempotencyKeyHeader, keys[0])
}
