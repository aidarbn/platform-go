package api

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// ClientIPMetadataKey is the gRPC metadata key that carries the client address from the
// REST gateway to the gRPC server. The gateway sets it from the address the HTTP side
// resolved and drops an HTTP header of the same name, so a REST client cannot choose
// it. The gRPC server believes it only from a trusted peer: loopback, the address the
// server itself listens on, or API_TRUSTED_PROXIES — a gRPC proxy in that list may set
// it too. From anyone else it is ignored and the peer address is the client.
//
// x-forwarded-for metadata cannot serve this purpose: grpc-gateway appends the address
// of the HTTP peer to it, so its last entry is the proxy, the same for every client,
// and its first entries are whatever the client sent.
const ClientIPMetadataKey = "x-platform-client-ip"

// legacyClientIPHeader is the header that carried the address before
// ClientIPMetadataKey. It is dropped from requests and never believed.
const legacyClientIPHeader = "X-Client-IP"

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
// resolved to the gRPC server under ClientIPMetadataKey.
func annotateClientIP(ctx context.Context, _ *http.Request) metadata.MD {
	ip, _ := ctx.Value(clientIPKey).(string)
	if ip == "" {
		return nil
	}
	return metadata.Pairs(ClientIPMetadataKey, ip)
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
}

func (c *clientIPResolver) trustedPeer(addr netip.Addr) bool {
	return addr.IsValid() && (addr.IsLoopback() || addr == c.self || isTrusted(addr, c.trusted))
}

// resolve returns ClientIPMetadataKey from a trusted peer — the last value, since the
// gateway annotator comes after forwarded headers — and the peer address otherwise.
func (c *clientIPResolver) resolve(ctx context.Context) string {
	host, addr := peerAddr(ctx)
	if !c.trustedPeer(addr) {
		return host
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(ClientIPMetadataKey); len(v) > 0 {
			if ip, ok := parseIP(v[len(v)-1]); ok {
				return ip.String()
			}
		}
	}
	return host
}

// unary puts the client address into the context first, so the rate limit, the logs
// and the handlers all see the same, checked address through ClientIP.
func (c *clientIPResolver) unary(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	return handler(context.WithValue(ctx, clientIPKey, c.resolve(ctx)), req)
}

func (c *clientIPResolver) stream(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx := context.WithValue(ss.Context(), clientIPKey, c.resolve(ss.Context()))
	return handler(srv, &contextStream{ServerStream: ss, ctx: ctx})
}

// contextStream is a server stream with a replaced context.
type contextStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextStream) Context() context.Context { return s.ctx }
