package api

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RateLimit is a token bucket per client address, with separate limits for public
// methods, as in taply, and a second bucket per principal for authenticated calls.
type RateLimit struct {
	RPS, PublicRPS     float64
	Burst, PublicBurst int

	// PrincipalRPS and PrincipalBurst limit each principal the project names with
	// RateLimitKey, whatever address the calls come from. Zero turns it off.
	PrincipalRPS   float64
	PrincipalBurst int
}

type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time // guarded by rateLimiter.mu
}

// tier is one bucket per key with its own rate.
type tier struct {
	rps     rate.Limit
	burst   int
	entries map[string]*limiterEntry
}

type rateLimiter struct {
	key      func(ctx context.Context) string // an empty key is not limited
	public   func(method string) bool         // methods of the public tier, when set
	mu       sync.Mutex
	normal   tier
	public_  tier
	limited  *prometheus.CounterVec
	stopOnce sync.Once
	done     chan struct{}
}

// newRateLimiter limits by client address: ClientIP, with an IPv6 address taken as its
// /64, the block a single client normally owns.
func newRateLimiter(cfg RateLimit, public func(string) bool, reg *prometheus.Registry) (*rateLimiter, error) {
	return newLimiter(func(ctx context.Context) string { return addressKey(ClientIP(ctx)) },
		tier{rps: rate.Limit(cfg.RPS), burst: cfg.Burst},
		public, tier{rps: rate.Limit(cfg.PublicRPS), burst: cfg.PublicBurst},
		prometheus.CounterOpts{Namespace: "api", Name: "rate_limited_total", Help: "calls refused by the rate limit"},
		reg)
}

// newPrincipalLimiter limits by the principal the project names; calls without one are
// left to the limit by address.
func newPrincipalLimiter(cfg RateLimit, key func(context.Context) string, reg *prometheus.Registry) (*rateLimiter, error) {
	return newLimiter(key, tier{rps: rate.Limit(cfg.PrincipalRPS), burst: cfg.PrincipalBurst}, nil, tier{},
		prometheus.CounterOpts{Namespace: "api", Name: "principal_rate_limited_total", Help: "calls refused by the rate limit per principal"},
		reg)
}

func newLimiter(key func(context.Context) string, normal tier, public func(string) bool, publicTier tier, opts prometheus.CounterOpts, reg *prometheus.Registry) (*rateLimiter, error) {
	normal.entries, publicTier.entries = map[string]*limiterEntry{}, map[string]*limiterEntry{}
	// A bucket that holds no token refuses every call: a burst under one is a mistake.
	normal.burst, publicTier.burst = max(normal.burst, 1), max(publicTier.burst, 1)
	l := &rateLimiter{
		key:     key,
		public:  public,
		normal:  normal,
		public_: publicTier,
		done:    make(chan struct{}),
		limited: prometheus.NewCounterVec(opts, []string{"method"}),
	}
	if err := reg.Register(l.limited); err != nil {
		return nil, err
	}
	go l.cleanup()
	return l, nil
}

// addressKey is the bucket of an address: IPv6 by its /64, since one subscriber gets a
// whole block and could otherwise take a new bucket with every address in it.
func addressKey(ip string) string {
	if ip == "" {
		return "unknown" // calls without a peer share one bucket, they are not let through
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is6() {
		return ip
	}
	return netip.PrefixFrom(addr, 64).Masked().String()
}

// cleanup forgets keys not seen for ten minutes, so the map does not grow with every
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
	for _, m := range []map[string]*limiterEntry{l.normal.entries, l.public_.entries} {
		for key, e := range m {
			if e.lastSeen.Before(before) {
				delete(m, key)
			}
		}
	}
}

func (l *rateLimiter) stop() { l.stopOnce.Do(func() { close(l.done) }) }

func (l *rateLimiter) allow(key, method string) bool {
	t := &l.normal
	if l.public != nil && l.public(method) {
		t = &l.public_
	}

	l.mu.Lock()
	e, ok := t.entries[key]
	if !ok {
		e = &limiterEntry{limiter: rate.NewLimiter(t.rps, t.burst)}
		t.entries[key] = e
	}
	e.lastSeen = time.Now()
	l.mu.Unlock()
	return e.limiter.Allow()
}

func (l *rateLimiter) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if key := l.key(ctx); key != "" && !l.allow(key, info.FullMethod) {
		l.limited.WithLabelValues(info.FullMethod).Inc()
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}
	return handler(ctx, req)
}
