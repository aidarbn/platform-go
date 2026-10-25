package api

import (
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func fromPeer(addr string, md ...string) context.Context {
	host, port, _ := net.SplitHostPort(addr)
	tcp := &net.TCPAddr{IP: net.ParseIP(host)}
	tcp.Port, _ = net.LookupPort("tcp", port)
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: tcp})
	if len(md) > 0 {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(md...))
	}
	return ctx
}

// A direct gRPC client that is not a trusted proxy cannot pick its own bucket by
// naming another address in metadata.
func TestForgedClientAddressShareABucket(t *testing.T) {
	l, err := newRateLimiter(RateLimit{RPS: 0.001, Burst: 1}, nil, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.stop)
	info := &grpc.UnaryServerInfo{FullMethod: "/x.v1.S/M"}
	ok := func(context.Context, any) (any, error) { return "ok", nil }
	for i, forged := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"} {
		ctx := fromPeer("203.0.113.5:4000", "x-client-ip", forged, "x-platform-client-ip", forged)
		_, err := l.unary(ctx, nil, info, ok)
		if i == 0 && err != nil {
			t.Fatalf("first call: %v", err)
		}
		if i > 0 && status.Code(err) != codes.ResourceExhausted {
			t.Errorf("call %d with a forged address %s got a new bucket: %v", i, forged, err)
		}
	}
}

func TestResolveGRPCClientIP(t *testing.T) {
	c := &clientIPResolver{
		trusted: []netip.Prefix{netip.MustParsePrefix("10.20.0.0/16")},
		self:    netip.MustParseAddr("192.0.2.10"),
	}
	cases := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"no peer", context.Background(), ""},
		{"untrusted peer, forged key", fromPeer("203.0.113.5:4000", ClientIPMetadataKey, "1.1.1.1"), "203.0.113.5"},
		{"untrusted peer, legacy key", fromPeer("203.0.113.5:4000", "x-client-ip", "1.1.1.1"), "203.0.113.5"},
		{"loopback gateway", fromPeer("127.0.0.1:4000", ClientIPMetadataKey, "198.51.100.7"), "198.51.100.7"},
		{"loopback v6", fromPeer("[::1]:4000", ClientIPMetadataKey, "2001:db8::7"), "2001:db8::7"},
		{"loopback, legacy key ignored", fromPeer("127.0.0.1:4000", "x-client-ip", "1.1.1.1"), "127.0.0.1"},
		{"loopback, no key", fromPeer("127.0.0.1:4000"), "127.0.0.1"},
		{"loopback, last value wins", fromPeer("127.0.0.1:4000", ClientIPMetadataKey, "1.1.1.1", ClientIPMetadataKey, "198.51.100.7"), "198.51.100.7"},
		{"loopback, garbage key", fromPeer("127.0.0.1:4000", ClientIPMetadataKey, "not-an-ip"), "127.0.0.1"},
		{"trusted proxy", fromPeer("10.20.3.4:4000", ClientIPMetadataKey, "198.51.100.7"), "198.51.100.7"},
		{"own address", fromPeer("192.0.2.10:4000", ClientIPMetadataKey, "198.51.100.7"), "198.51.100.7"},
		{"untrusted v6 peer", fromPeer("[2001:db8::1]:4000", ClientIPMetadataKey, "1.1.1.1"), "2001:db8::1"},
		{"mapped v4 peer", fromPeer("[::ffff:203.0.113.5]:4000"), "203.0.113.5"},
	}
	for _, tc := range cases {
		if got := c.resolve(tc.ctx); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}

	// A peer that is not an IP address, such as a unix socket, is named as it is.
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.UnixAddr{Name: "/run/api.sock", Net: "unix"}})
	if got := c.resolve(metadata.NewIncomingContext(ctx, metadata.Pairs(ClientIPMetadataKey, "1.1.1.1"))); got != "/run/api.sock" {
		t.Errorf("unix peer: %q", got)
	}
}

// Without the interceptor, outside a server, ClientIP is the peer and never metadata.
func TestClientIPIgnoresMetadataWithoutTheInterceptor(t *testing.T) {
	if got := ClientIP(fromPeer("127.0.0.1:1", ClientIPMetadataKey, "1.1.1.1", "x-client-ip", "2.2.2.2")); got != "127.0.0.1" {
		t.Errorf("ClientIP = %q", got)
	}
}

func TestParseIP(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4":             "1.2.3.4",
		" 1.2.3.4 ":           "1.2.3.4",
		"1.2.3.4:5678":        "1.2.3.4",
		"2001:db8::1":         "2001:db8::1",
		"[2001:db8::1]":       "2001:db8::1",
		"[2001:db8::1]:443":   "2001:db8::1",
		"::ffff:1.2.3.4":      "1.2.3.4",
		"fe80::1%eth0":        "fe80::1",
		"unknown":             "",
		"":                    "",
		"1.2.3.4:5678:9":      "",
		"host.example:80":     "",
		"[not-an-ip]:80":      "",
		"1.2.3.4, 5.6.7.8":    "",
		"300.1.1.1":           "",
		"[2001:db8::1]:80:80": "",
	}
	for in, want := range cases {
		addr, ok := parseIP(in)
		got := ""
		if ok {
			got = addr.String()
		}
		if got != want {
			t.Errorf("parseIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveHTTPClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	cases := []struct {
		name, remote string
		xff          []string
		want         string
	}{
		{"direct", "203.0.113.5:1000", nil, "203.0.113.5"},
		{"untrusted with xff", "203.0.113.5:1000", []string{"1.1.1.1"}, "203.0.113.5"},
		{"trusted, last hop", "10.0.0.2:1000", []string{"1.1.1.1, 198.51.100.7"}, "198.51.100.7"},
		{"trusted, last line", "10.0.0.2:1000", []string{"1.1.1.1", "198.51.100.7"}, "198.51.100.7"},
		{"trusted, hop with port", "10.0.0.2:1000", []string{"198.51.100.7:4321"}, "198.51.100.7"},
		{"trusted, v6 hop", "10.0.0.2:1000", []string{"[2001:db8::7]:4321"}, "2001:db8::7"},
		{"trusted, garbage hop", "10.0.0.2:1000", []string{"1.1.1.1, nonsense"}, "10.0.0.2"},
		{"trusted, empty hop", "10.0.0.2:1000", []string{"1.1.1.1,"}, "10.0.0.2"},
		{"v6 loopback proxy", "[::1]:1000", []string{"198.51.100.7"}, "198.51.100.7"},
		{"mapped v4 proxy", "[::ffff:10.0.0.2]:1000", []string{"198.51.100.7"}, "198.51.100.7"},
		{"not an address", "@", []string{"198.51.100.7"}, "@"},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.remote
		for _, line := range tc.xff {
			r.Header.Add("X-Forwarded-For", line)
		}
		if got := resolveClientIP(r, trusted); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAnnotateClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if md := annotateClientIP(context.Background(), r); md != nil {
		t.Errorf("no address: %v", md)
	}
	md := annotateClientIP(context.WithValue(context.Background(), clientIPKey, "198.51.100.7"), r)
	if got := md.Get(ClientIPMetadataKey); len(got) != 1 || got[0] != "198.51.100.7" {
		t.Errorf("md = %v", md)
	}
}

func TestAddressKey(t *testing.T) {
	cases := map[string]string{
		"":                     "unknown",
		"198.51.100.7":         "198.51.100.7",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff::1": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":      "2001:db8:1:3::/64",
		"/run/api.sock":        "/run/api.sock",
	}
	for in, want := range cases {
		if got := addressKey(in); got != want {
			t.Errorf("addressKey(%q) = %q, want %q", in, got, want)
		}
	}
}

type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s fakeStream) Context() context.Context { return s.ctx }

func TestClientIPOnStreams(t *testing.T) {
	c := &clientIPResolver{}
	var got string
	err := c.stream(nil, fakeStream{ctx: fromPeer("127.0.0.1:1", ClientIPMetadataKey, "198.51.100.7")}, &grpc.StreamServerInfo{},
		func(_ any, ss grpc.ServerStream) error {
			got = ClientIP(ss.Context())
			return nil
		})
	if err != nil || got != "198.51.100.7" {
		t.Errorf("stream sees %q, %v", got, err)
	}
}

// Calls of one client and the cleanup run at the same time; run with -race.
func TestRateLimiterConcurrentUse(t *testing.T) {
	l, err := newRateLimiter(RateLimit{RPS: 1000, Burst: 1000, PublicRPS: 1000, PublicBurst: 1000},
		func(m string) bool { return m == "/p" }, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.stop)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 200 {
				method := "/n"
				if j%2 == 0 {
					method = "/p"
				}
				l.allow(fmt.Sprintf("10.0.0.%d", i), method)
			}
		})
	}
	wg.Go(func() {
		for range 200 {
			l.forget(time.Now().Add(time.Hour)) // forgets everything seen so far
		}
	})
	wg.Wait()
	l.forget(time.Now().Add(time.Hour))
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.normal.entries)+len(l.public_.entries) != 0 {
		t.Error("stale entries are kept")
	}
}

// An idle key is forgotten, a recent one is kept.
func TestRateLimiterForget(t *testing.T) {
	l, err := newRateLimiter(RateLimit{RPS: 1, Burst: 1}, nil, prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.stop)
	l.allow("old", "/m")
	l.mu.Lock()
	l.normal.entries["old"].lastSeen = time.Now().Add(-time.Hour)
	l.mu.Unlock()
	l.allow("new", "/m")
	l.forget(time.Now().Add(-10 * time.Minute))
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.normal.entries["old"]; ok {
		t.Error("an idle key is kept")
	}
	if _, ok := l.normal.entries["new"]; !ok {
		t.Error("a recent key is forgotten")
	}
}

func TestLimiterMetricRegisteredTwice(t *testing.T) {
	reg := prometheus.NewRegistry()
	l, err := newPrincipalLimiter(RateLimit{PrincipalRPS: 1, PrincipalBurst: 1}, func(context.Context) string { return "u" }, reg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.stop)
	if _, err := newPrincipalLimiter(RateLimit{PrincipalRPS: 1, PrincipalBurst: 1}, func(context.Context) string { return "u" }, reg); err == nil {
		t.Error("a second registration of the metric is accepted")
	}
}

// The gateway never forwards the client address headers, even when a request reaches it
// without identify.
func TestForwardHeaderDropsClientAddress(t *testing.T) {
	for _, h := range []string{"X-Platform-Client-IP", "x-platform-client-ip", "X-Client-IP", "X-Client-Ip"} {
		if _, ok := forwardHeader(h); ok {
			t.Errorf("%s is forwarded", h)
		}
	}
	if got, ok := forwardHeader("X-Request-ID"); !ok || got != "x-request-id" {
		t.Errorf("X-Request-ID: %q %v", got, ok)
	}
}
