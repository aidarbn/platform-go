package api

import (
	"context"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RateLimit is a token bucket per client address, with separate limits for public
// methods, as in taply.
type RateLimit struct {
	RPS, PublicRPS     float64
	Burst, PublicBurst int
}

type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type rateLimiter struct {
	cfg      RateLimit
	public   func(method string) bool
	mu       sync.Mutex
	clients  map[string]*limiterEntry
	public_  map[string]*limiterEntry
	limited  *prometheus.CounterVec
	stopOnce sync.Once
	done     chan struct{}
}

func newRateLimiter(cfg RateLimit, public func(string) bool, reg *prometheus.Registry) (*rateLimiter, error) {
	l := &rateLimiter{
		cfg:     cfg,
		public:  public,
		clients: map[string]*limiterEntry{},
		public_: map[string]*limiterEntry{},
		done:    make(chan struct{}),
		limited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "api", Name: "rate_limited_total", Help: "calls refused by the rate limit",
		}, []string{"method"}),
	}
	if err := reg.Register(l.limited); err != nil {
		return nil, err
	}
	go l.cleanup()
	return l, nil
}

// cleanup forgets clients not seen for ten minutes, so the map does not grow with every
// address that ever called.
func (l *rateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-l.done:
			return
		case now := <-ticker.C:
			l.forget(now.Add(-10 * time.Minute))
		}
	}
}

func (l *rateLimiter) forget(before time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range []map[string]*limiterEntry{l.clients, l.public_} {
		for ip, e := range m {
			if e.lastSeen.Before(before) {
				delete(m, ip)
			}
		}
	}
}

func (l *rateLimiter) stop() { l.stopOnce.Do(func() { close(l.done) }) }

func (l *rateLimiter) allow(ip, method string) bool {
	entries, rps, burst := l.clients, l.cfg.RPS, l.cfg.Burst
	if l.public != nil && l.public(method) {
		entries, rps, burst = l.public_, l.cfg.PublicRPS, l.cfg.PublicBurst
	}

	l.mu.Lock()
	e, ok := entries[ip]
	if !ok {
		e = &limiterEntry{limiter: rate.NewLimiter(rate.Limit(rps), burst)}
		entries[ip] = e
	}
	e.lastSeen = time.Now()
	l.mu.Unlock()
	return e.limiter.Allow()
}

func (l *rateLimiter) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if !l.allow(ClientIP(ctx), info.FullMethod) {
		l.limited.WithLabelValues(info.FullMethod).Inc()
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
	return handler(ctx, req)
}
