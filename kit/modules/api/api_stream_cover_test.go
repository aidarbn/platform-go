package api_test

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"

	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/platform"
)

// Reflection is a streaming service, so it shows the stream chain at work: a project
// stream interceptor sees the call, and a panic in the chain answers Internal instead
// of taking the server down.
func TestStreamInterceptorsWithReflection(t *testing.T) {
	var streams atomic.Int32
	extraWire = func(app *platform.App) {
		api.AddStreamInterceptor(app, func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			streams.Add(1)
			md, _ := metadata.FromIncomingContext(ss.Context())
			if len(md.Get("x-break")) > 0 {
				panic("the stream interceptor broke")
			}
			return h(srv, ss)
		})
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{Reflection: true})

	conn, err := grpc.NewClient(s.module.GRPCAddr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := reflectionpb.NewServerReflectionClient(conn)

	list := func(ctx context.Context) ([]string, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		stream, err := client.ServerReflectionInfo(ctx)
		if err != nil {
			return nil, err
		}
		if err := stream.Send(&reflectionpb.ServerReflectionRequest{
			MessageRequest: &reflectionpb.ServerReflectionRequest_ListServices{},
		}); err != nil {
			return nil, err
		}
		resp, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		var names []string
		for _, svc := range resp.GetListServicesResponse().GetService() {
			names = append(names, svc.GetName())
		}
		_ = stream.CloseSend()
		return names, nil
	}

	names, err := list(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(names, " "), "platformtest.v1.EchoService") {
		t.Fatalf("services = %v", names)
	}
	if streams.Load() != 1 {
		t.Fatalf("the project stream interceptor ran %d times", streams.Load())
	}

	_, err = list(metadata.AppendToOutgoingContext(context.Background(), "x-break", "1"))
	if status.Code(err) != codes.Internal {
		t.Fatalf("a panicking stream: %v, want Internal", err)
	}
	if !strings.Contains(s.logs.String(), "panic while handling a stream") {
		t.Errorf("the panic is not logged:\n%s", s.logs.String())
	}
	// The server survived.
	if _, err := list(context.Background()); err != nil {
		t.Fatalf("after the panic: %v", err)
	}
}

// With a user function, keys are per caller: two users may send the same key and both
// get their own call.
func TestIdempotencyKeysArePerUser(t *testing.T) {
	extraWire = func(app *platform.App) {
		api.IdempotencyUser(app, func(ctx context.Context) string {
			md, _ := metadata.FromIncomingContext(ctx)
			return strings.Join(md.Get("x-user"), "")
		})
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{}, api.WithIdempotencyStore(api.NewMemoryIdempotencyStore()))
	thingCalls.Store(0)

	call := func(user string) string {
		code, body, _ := s.do("POST", "/v1/things", `{"name":"a"}`, map[string]string{"Idempotency-Key": "same", "X-User": user})
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", user, code, body)
		}
		return body
	}
	alice, bob, aliceAgain := call("alice"), call("bob"), call("alice")
	if thingCalls.Load() != 2 || alice != aliceAgain || alice == bob {
		t.Fatalf("calls %d\nalice %s\nbob %s\nalice again %s", thingCalls.Load(), alice, bob, aliceAgain)
	}
}

func TestMemoryIdempotencyStoreDeletesExpired(t *testing.T) {
	ctx := context.Background()
	store := api.NewMemoryIdempotencyStore()
	now := time.Now()
	for key, expires := range map[string]time.Time{"old": now.Add(-time.Minute), "fresh": now.Add(time.Hour)} {
		if _, created, err := store.Begin(ctx, api.IdempotencyRecord{Method: "/m", Key: key, ExpiresAt: expires}); err != nil || !created {
			t.Fatalf("Begin %s: %v %v", key, created, err)
		}
	}
	if n, err := store.DeleteExpired(ctx, now); err != nil || n != 1 {
		t.Fatalf("DeleteExpired = %d, %v", n, err)
	}
	// The expired key is free again, the fresh one is still taken.
	if _, created, _ := store.Begin(ctx, api.IdempotencyRecord{Method: "/m", Key: "old"}); !created {
		t.Error("the expired key is still taken")
	}
	if _, created, _ := store.Begin(ctx, api.IdempotencyRecord{Method: "/m", Key: "fresh"}); created {
		t.Error("the fresh key was removed")
	}
	// Retake of a record that does not exist takes nothing.
	if ok, err := store.Retake(ctx, 999, now); ok || err != nil {
		t.Errorf("Retake of a missing record = %v, %v", ok, err)
	}
}

type unixAddr string

func (a unixAddr) Network() string { return "unix" }
func (a unixAddr) String() string  { return string(a) }

// Without the gateway's header ClientIP falls back to the connection of the gRPC call.
func TestClientIPFromThePeer(t *testing.T) {
	ctx := context.Background()
	if ip := api.ClientIP(ctx); ip != "" {
		t.Errorf("no peer: %q", ip)
	}
	tcp := peer.NewContext(ctx, &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 5555}})
	if ip := api.ClientIP(tcp); ip != "203.0.113.7" {
		t.Errorf("tcp peer: %q", ip)
	}
	sock := peer.NewContext(ctx, &peer.Peer{Addr: unixAddr("/run/api.sock")})
	if ip := api.ClientIP(sock); ip != "/run/api.sock" {
		t.Errorf("unix peer: %q", ip)
	}
}

// Plain routes stream through the access log writer and are guarded by the recovery.
func TestPlainRoutesFlushAndRecover(t *testing.T) {
	extraWire = func(app *platform.App) {
		api.HandleHTTP(app, "GET /stream", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rc := http.NewResponseController(w)
			_, _ = w.Write([]byte("part one;"))
			if err := rc.Flush(); err != nil {
				http.Error(w, "flush: "+err.Error(), http.StatusInternalServerError)
				return
			}
			// The deadline reaches the connection through Unwrap.
			if err := rc.SetWriteDeadline(time.Now().Add(time.Minute)); err != nil {
				_, _ = w.Write([]byte("deadline: " + err.Error()))
				return
			}
			_, _ = w.Write([]byte("part two"))
		}))
		api.HandleHTTP(app, "GET /broken", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("the route broke")
		}))
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{})

	if code, body, _ := s.do("GET", "/stream", "", nil); code != http.StatusOK || body != "part one;part two" {
		t.Fatalf("stream: %d %q", code, body)
	}
	if code, body, _ := s.do("GET", "/broken", "", nil); code != http.StatusInternalServerError || !strings.Contains(body, "internal error") {
		t.Fatalf("broken: %d %q", code, body)
	}
	if !strings.Contains(s.logs.String(), "panic while handling an HTTP request") {
		t.Errorf("the panic is not logged:\n%s", s.logs.String())
	}
}
