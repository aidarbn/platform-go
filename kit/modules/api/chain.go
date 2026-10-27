package api

import (
	"cmp"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strings"

	"google.golang.org/grpc"

	"github.com/aidarbn/platform-go/kit/platform"
)

// Phase places an interceptor in the chain of the gRPC server. The chain runs the phases
// from the outside in, in the order of their values; inside a phase the platform's
// interceptors come first, then the project's in the order they were added. The phases
// are a hundred apart, so a project that needs a place between two of them uses an
// offset, such as PhaseGuard+10.
//
// The order is taply's: recovery, metrics and logging, the rate limit by address, the
// project's authentication, access checks and guards, request transforms, validation,
// idempotency, and a second recovery around the handler.
type Phase int

const (
	// PhaseRecover is the outer recovery: a panic in any interceptor becomes Internal.
	PhaseRecover Phase = iota * 100
	// PhaseObserve settles the client address, then metrics, logging and hiding errors
	// without a status behind a plain Internal.
	PhaseObserve
	// PhaseLimit is the rate limit by address and the count of deprecated methods.
	PhaseLimit
	// PhaseErrorsI18n translates error messages; it is outside authentication, so the
	// refusals of the project's authentication are translated too.
	PhaseErrorsI18n
	// PhaseAuthenticate is the project's authentication: it puts the caller into the
	// context, for rbac with rbac.WithRoles. AddUnaryInterceptor and
	// AddStreamInterceptor add here.
	PhaseAuthenticate
	// PhaseAuthorize is the access check; Authorize adds here, and the rbac module
	// through it.
	PhaseAuthorize
	// PhaseGuard is for checks that need the caller, such as the tenant a caller may
	// act for; the rate limit per principal runs here.
	PhaseGuard
	// PhaseTransform changes the request and the response with the caller known: the
	// translation of response fields, string normalisation.
	PhaseTransform
	// PhaseValidate checks the request against its protovalidate rules.
	PhaseValidate
	// PhaseAfterValidate sees a request that passed validation.
	PhaseAfterValidate
	// PhaseIdempotency remembers the outcome of a call with an Idempotency-Key.
	PhaseIdempotency
	// PhaseHandlerRecover is the inner recovery: a panic of the handler becomes Internal
	// before idempotency, which remembers it instead of holding the key.
	PhaseHandlerRecover
)

var phaseNames = map[Phase]string{
	PhaseRecover:        "recover",
	PhaseObserve:        "observe",
	PhaseLimit:          "limit",
	PhaseErrorsI18n:     "errors-i18n",
	PhaseAuthenticate:   "authenticate",
	PhaseAuthorize:      "authorize",
	PhaseGuard:          "guard",
	PhaseTransform:      "transform",
	PhaseValidate:       "validate",
	PhaseAfterValidate:  "after-validate",
	PhaseIdempotency:    "idempotency",
	PhaseHandlerRecover: "handler-recover",
}

// String names the phase; an offset from a named phase reads as "guard+10".
func (p Phase) String() string {
	if name, ok := phaseNames[p]; ok {
		return name
	}
	base := p - p%100
	if p < 0 || base > PhaseHandlerRecover {
		return fmt.Sprintf("phase(%d)", int(p))
	}
	return fmt.Sprintf("%s+%d", phaseNames[base], int(p-base))
}

// checkPhase refuses a phase before the outer recovery: a panic there would take the
// process down.
func checkPhase(p Phase) {
	if p < PhaseRecover {
		panic(fmt.Sprintf("api: phase %d is before PhaseRecover, outside every recovery", int(p)))
	}
}

// chainEntry is one interceptor in the chain, unary, stream or both.
type chainEntry struct {
	phase  Phase
	name   string
	unary  grpc.UnaryServerInterceptor
	stream grpc.StreamServerInterceptor
}

// AddUnaryInterceptorAt adds a project interceptor in the given phase. The name shows in
// Chain and in the log line of the chain at start; an empty one is taken from the
// function. A phase below PhaseRecover would run outside every recovery and panics; one
// above PhaseHandlerRecover runs inside the handler's recovery-free zone, covered by the
// outer recovery only.
//
//	api.AddUnaryInterceptorAt(app, api.PhaseGuard, "restaurant", restaurantGuard)
func AddUnaryInterceptorAt(app *platform.App, p Phase, name string, i grpc.UnaryServerInterceptor) {
	if i == nil {
		panic("api: a nil unary interceptor")
	}
	checkPhase(p)
	r := registry(app)
	r.add(func() { r.entries = append(r.entries, chainEntry{phase: p, name: entryName(name, i), unary: i}) })
}

// AddStreamInterceptorAt adds a project stream interceptor in the given phase.
func AddStreamInterceptorAt(app *platform.App, p Phase, name string, i grpc.StreamServerInterceptor) {
	if i == nil {
		panic("api: a nil stream interceptor")
	}
	checkPhase(p)
	r := registry(app)
	r.add(func() { r.entries = append(r.entries, chainEntry{phase: p, name: entryName(name, i), stream: i}) })
}

// Chain lists the unary interceptors of the server from the outside in, as "phase/name",
// for tests of a project and for reading the order. It is filled when the module starts.
func Chain(app *platform.App) []string {
	r := registry(app)
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.chain)
}

// StreamChain lists the stream interceptors the same way.
func StreamChain(app *platform.App) []string {
	r := registry(app)
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.streamChain)
}

// buildChain orders the platform's interceptors and the project's by phase; within a
// phase the platform's come first and the rest keep the order they were added in.
func buildChain(platformEntries, project []chainEntry) (unary []grpc.UnaryServerInterceptor, stream []grpc.StreamServerInterceptor, unaryNames, streamNames []string) {
	all := slices.Concat(platformEntries, project)
	slices.SortStableFunc(all, func(a, b chainEntry) int { return cmp.Compare(a.phase, b.phase) })
	for _, e := range all {
		label := e.phase.String() + "/" + e.name
		if e.unary != nil {
			unary = append(unary, e.unary)
			unaryNames = append(unaryNames, label)
		}
		if e.stream != nil {
			stream = append(stream, e.stream)
			streamNames = append(streamNames, label)
		}
	}
	return unary, stream, unaryNames, streamNames
}

// entryName is the given name, or the name of the function without its import path.
func entryName(name string, fn any) string {
	if name != "" {
		return name
	}
	if f := runtime.FuncForPC(reflect.ValueOf(fn).Pointer()); f != nil {
		full := f.Name()
		return full[strings.LastIndex(full, "/")+1:]
	}
	return "interceptor"
}
