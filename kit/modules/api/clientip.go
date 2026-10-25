package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// ClientIPMetadataKey is the gRPC metadata key that carries the client address from the
// REST gateway to the gRPC server. The gateway sets it from the address the HTTP side
// resolved and drops an HTTP header of the same name, so a REST client cannot choose
// it. The gRPC server believes it only from a trusted peer: loopback, the address the
// server itself listens on, or the networks set in API_TRUSTED_PROXIES — a gRPC proxy
// there may set it too; the default of every private network does not count. From
// anyone else it is ignored and the peer address is the client.
//
// x-forwarded-for metadata cannot serve this purpose: grpc-gateway appends the address
// of the HTTP peer to it, so its last entry is the proxy, the same for every client,
// and its first entries are whatever the client sent.
const ClientIPMetadataKey = "x-platform-client-ip"

// legacyClientIPHeader is the header that carried the address before
// ClientIPMetadataKey, and legacyClientIPKey its metadata key. The HTTP side still sets
// the header to the resolved address for plain routes, overwriting whatever the client
// sent; the gateway never forwards it, but passes the resolved address under both keys.
// The gRPC server reads the old key, from trusted peers only, when the new one is
// missing: a gRPC proxy or a client of the same host written for an older release.
const (
	legacyClientIPHeader = "X-Client-IP"
	legacyClientIPKey    = "x-client-ip"
)

// ClientIP returns the address of the client. For a REST call it is the address the
// HTTP side resolved: the last X-Forwarded-For hop when the connection comes from a
// trusted proxy — the address the proxy itself saw, which the client cannot forge —
// and the connection address otherwise. For a direct gRPC call it is the peer address,
// or ClientIPMetadataKey when the peer is trusted.
func ClientIP(ctx context.Context) string {
	if ip, ok := ctx.Value(clientIPKey).(string); ok {
		return ip
	}
	host, _ := peerAddr(ctx)
	return host
}

// resolveClientIP returns the connection address, or the last address of
// X-Forwarded-For when the connection comes from a trusted proxy. Only the last hop is
// taken: the trusted proxies default to every private network, where a client may sit
// too, so walking further left would let such a client name any address. A proxy that
// adds its own header line, as HAProxy does, is read from the last line.
func resolveClientIP(r *http.Request, trusted []netip.Prefix) string {
	remote := r.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	addr, ok := parseIP(remote)
	if !ok {
		return remote
	}
	remote = addr.String()
	lines := r.Header.Values("X-Forwarded-For")
	if len(lines) == 0 || !isTrusted(addr, trusted) {
		return remote
	}
	hops := strings.Split(lines[len(lines)-1], ",")
	if ip, ok := parseIP(hops[len(hops)-1]); ok {
		return ip.String()
	}
	return remote
}

// parseIP reads an address as proxies write it: plain, in brackets, with a port, with
// an IPv6 zone. IPv4 mapped into IPv6 becomes plain IPv4, so one client is one key.
func parseIP(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	addr, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"))
	if err != nil {
		host, _, serr := net.SplitHostPort(s)
		if serr != nil {
			return netip.Addr{}, false
		}
		if addr, err = netip.ParseAddr(host); err != nil {
			return netip.Addr{}, false
		}
	}
	return addr.Unmap().WithZone(""), true
}

func isTrusted(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// annotateClientIP is a gateway metadata annotator: it hands the address the HTTP side
// resolved to the gRPC server under ClientIPMetadataKey, and under the old key too, for
// handlers that read the metadata themselves.
func annotateClientIP(ctx context.Context, _ *http.Request) metadata.MD {
	ip, _ := ctx.Value(clientIPKey).(string)
	if ip == "" {
		return nil
	}
	return metadata.Pairs(ClientIPMetadataKey, ip, legacyClientIPKey, ip)
}

// peerAddr returns the host of the gRPC peer, and the parsed address when it is one.
func peerAddr(ctx context.Context) (string, netip.Addr) {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return "", netip.Addr{}
	}
	host := p.Addr.String()
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	addr, ok := parseIP(host)
	if !ok {
		return host, netip.Addr{}
	}
	return addr.String(), addr
}

// clientIPResolver decides the client address of a gRPC call.
type clientIPResolver struct {
	trusted []netip.Prefix
	// self is the address the gRPC server listens on, when it is a specific one: the
	// gateway in this process then connects from it rather than from loopback. It is
	// set before the server serves.
	self netip.Addr

	log        *slog.Logger
	legacyOnce sync.Once
}

func (c *clientIPResolver) trustedPeer(addr netip.Addr) bool {
	return addr.IsValid() && (addr.IsLoopback() || addr == c.self || isTrusted(addr, c.trusted))
}

// resolve returns ClientIPMetadataKey from a trusted peer — the last value, since the
// gateway annotator comes after forwarded headers —, the old x-client-ip key from a
// trusted peer that sent no new one, and the peer address otherwise.
func (c *clientIPResolver) resolve(ctx context.Context) string {
	host, addr := peerAddr(ctx)
	if !c.trustedPeer(addr) {
		return host
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get(ClientIPMetadataKey); len(v) > 0 {
		if ip, ok := parseIP(v[len(v)-1]); ok {
			return ip.String()
		}
		return host
	}
	if v := md.Get(legacyClientIPKey); len(v) > 0 {
		if ip, ok := parseIP(v[len(v)-1]); ok {
			c.legacyOnce.Do(func() {
				if c.log != nil {
					c.log.Warn("a trusted gRPC peer names the client with the old x-client-ip metadata; send "+ClientIPMetadataKey+" instead", "peer", host)
				}
			})
			return ip.String()
		}
	}
	return host
}

// unary puts the client address into the context first, so the rate limit, the log of
// deprecated calls and the handlers all see the same, checked address through ClientIP.
func (c *clientIPResolver) unary(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	return handler(c.settle(ctx), req)
}

func (c *clientIPResolver) stream(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	return handler(srv, &contextStream{ServerStream: ss, ctx: c.settle(ss.Context())})
}

// settle puts the checked address into the context, and into both address keys of the
// incoming metadata, so a handler that reads the metadata itself, as code for older
// releases did, sees the checked address too and never one an untrusted peer sent.
func (c *clientIPResolver) settle(ctx context.Context) context.Context {
	ip := c.resolve(ctx)
	md, _ := metadata.FromIncomingContext(ctx)
	md = md.Copy()
	if ip == "" {
		md.Delete(ClientIPMetadataKey)
		md.Delete(legacyClientIPKey)
	} else {
		md.Set(ClientIPMetadataKey, ip)
		md.Set(legacyClientIPKey, ip)
	}
	return context.WithValue(metadata.NewIncomingContext(ctx, md), clientIPKey, ip)
}

// contextStream is a server stream with a replaced context.
type contextStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextStream) Context() context.Context { return s.ctx }
