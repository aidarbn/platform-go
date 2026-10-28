package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/genproto/googleapis/api/annotations"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/aidarbn/platform-go/kit/pgdb"
)

// IdempotencyKeyHeader is the header a client sends to make a call safe to repeat.
const IdempotencyKeyHeader = "Idempotency-Key"

// Statuses of an idempotency record, as in taply.
const (
	idemInProgress = "in_progress"
	idemCompleted  = "completed"
	idemRetry      = "retry"
)

// maxIdempotencyKey is the longest key: clients send UUIDs, and a longer one would not
// fit the unique index.
const maxIdempotencyKey = 255

// idempotencySaveTimeout bounds the write of an outcome. The write runs on a context
// cut loose from the call, so a client that hangs up does not leave the key in progress.
const idempotencySaveTimeout = 5 * time.Second

// leaseExtendTimeout bounds one extension of a lease.
const leaseExtendTimeout = 5 * time.Second

// IdempotencyRecord is one remembered call.
type IdempotencyRecord struct {
	ID          int64
	User        string
	Method      string
	Key         string
	Fingerprint string
	Status      string
	Code        int32
	Message     string
	// Response is the response message for a success and the google.rpc.Status the
	// client got for a failure, both in the binary proto format. Records written before
	// the status was kept have none for a failure, and are replayed from Code and
	// Message.
	Response  []byte
	ExpiresAt time.Time
	// Lease is the expires_at of the in_progress record the running call holds: the
	// token Finish checks, so a call whose key was taken over does not overwrite the
	// outcome of the one that took it. Zero means no check.
	Lease time.Time
}

// IdempotencyStore keeps the records. The postgres store is used when the postgres
// module is enabled; tests use the memory one.
type IdempotencyStore interface {
	// Begin inserts an in_progress record, or returns the existing one with created false.
	Begin(ctx context.Context, rec IdempotencyRecord) (IdempotencyRecord, bool, error)
	// Retake moves a retry record, an in_progress one whose lock expired or a completed
	// one past its expiry back to in_progress until lockUntil. It reports false when
	// another call took it first.
	Retake(ctx context.Context, id int64, lockUntil time.Time) (bool, error)
	// Finish stores the outcome. With rec.Lease set it writes only while the record is
	// still in_progress with that expiry, and returns ErrIdempotencyLeaseLost otherwise.
	Finish(ctx context.Context, rec IdempotencyRecord) error
	// DeleteExpired removes records past their expiry.
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
}

// IdempotencyLeaseExtender is a store that extends the lease of a running call, so a
// handler that runs longer than the lease keeps its key. The memory and postgres stores
// implement it; with a store that does not, the lease is not extended.
type IdempotencyLeaseExtender interface {
	// ExtendLease moves the expiry of an in_progress record from lease to until, and
	// reports false when the record no longer has that lease: it was taken over.
	ExtendLease(ctx context.Context, id int64, lease, until time.Time) (bool, error)
}

// ErrIdempotencyLeaseLost is returned by Finish when the record was taken over by a
// repeat after the lease expired: the late outcome is not written.
var ErrIdempotencyLeaseLost = errors.New("idempotency lease lost")

// RetryableError marks an error after which a repeated call must run again instead of
// getting the cached failure — or, returning false, a failure to remember although its
// code would let a repeat run.
type RetryableError interface{ IsRetryable() bool }

// cachedCodes are the outcomes a repeat gets back: a success and the client errors a
// repeat with the same body cannot fix. Everything else — Internal, Unknown, Canceled,
// Unavailable, DeadlineExceeded, ResourceExhausted, Aborted, DataLoss, a panic — is a
// passing failure, and a repeat with the key runs again. As in taply.
var cachedCodes = map[codes.Code]bool{
	codes.OK:                 true,
	codes.InvalidArgument:    true,
	codes.NotFound:           true,
	codes.AlreadyExists:      true,
	codes.FailedPrecondition: true,
	codes.PermissionDenied:   true,
	codes.Unauthenticated:    true,
	codes.OutOfRange:         true,
	codes.Unimplemented:      true,
}

// Results of api_idempotency_total.
const (
	idemReplay     = "replay"   // a repeat got the remembered outcome
	idemAborted    = "aborted"  // a repeat while the call still runs
	idemMismatch   = "mismatch" // the same key with another body
	idemLost       = "lease_lost"
	idemStored     = "stored" // a final outcome remembered
	idemRetried    = "retry"  // a passing failure: the next repeat runs again
	idemSaveFailed = "save_failed"
)

type idempotency struct {
	store     IdempotencyStore
	required  map[string]bool
	accept    map[string]bool // the methods whose keys count; filled once the services are known
	user      func(context.Context) string
	retention time.Duration
	lease     time.Duration
	log       *slog.Logger
	now       func() time.Time
	calls     *prometheus.CounterVec
}

func (i *idempotency) count(method, result string) {
	if i.calls != nil {
		i.calls.WithLabelValues(method, result).Inc()
	}
}

// leaseUntil is the expiry of a lease taken now, at the precision the database keeps,
// so the token read back is the one written.
func (i *idempotency) leaseUntil() time.Time {
	return i.now().Add(i.lease).Truncate(time.Microsecond)
}

func (i *idempotency) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	key := idempotencyKey(ctx)
	if key == "" {
		if i.required[info.FullMethod] {
			return nil, status.Errorf(codes.InvalidArgument, "%s is required in the header", IdempotencyKeyHeader)
		}
		return handler(ctx, req)
	}
	// A key counts only on the methods it is meant for and only for a known caller:
	// anonymous callers would share one space of keys and get each other's answers.
	if !i.required[info.FullMethod] && !i.accept[info.FullMethod] {
		return handler(ctx, req)
	}
	user := ""
	if i.user != nil {
		user = i.user(ctx)
	}
	if user == "" {
		return handler(ctx, req)
	}
	if len(key) > maxIdempotencyKey {
		return nil, status.Errorf(codes.InvalidArgument, "%s is longer than %d characters", IdempotencyKeyHeader, maxIdempotencyKey)
	}
	if !printableASCII(key) {
		return nil, status.Errorf(codes.InvalidArgument, "%s must be printable ASCII", IdempotencyKeyHeader)
	}

	msg, ok := req.(proto.Message)
	if !ok {
		return handler(ctx, req)
	}
	fingerprint, err := fingerprintOf(msg)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}

	lease := i.leaseUntil()
	rec, created, err := i.store.Begin(ctx, IdempotencyRecord{
		User: user, Method: info.FullMethod, Key: key, Fingerprint: fingerprint,
		Status: idemInProgress, ExpiresAt: lease,
	})
	if err != nil {
		i.log.ErrorContext(ctx, "idempotency check failed", "method", info.FullMethod, "err", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	if created {
		lease = rec.ExpiresAt
	} else {
		var replay bool
		if rec, replay, err = i.takeOver(ctx, rec, fingerprint, lease); err != nil || replay {
			if replay {
				i.count(info.FullMethod, idemReplay)
				return i.cached(ctx, info.FullMethod, rec)
			}
			if st, ok := status.FromError(err); ok && st.Code() == codes.AlreadyExists {
				i.count(info.FullMethod, idemMismatch)
			} else if ok && st.Code() == codes.Aborted {
				i.count(info.FullMethod, idemAborted)
			}
			return nil, err
		}
		lease = rec.Lease
	}
	rec.Lease = lease

	// The lease is extended while the handler runs: a repeat after it expired would
	// take the key over and run the call a second time.
	held := i.hold(ctx, rec)
	defer func() {
		// A panic that got past the handler's recovery, from an interceptor placed after
		// it: the key is released for a repeat instead of being held until the lease
		// runs out.
		if p := recover(); p != nil {
			i.finish(ctx, rec, held, nil, status.Error(codes.Internal, "internal error"))
			panic(p)
		}
	}()
	resp, handlerErr := handler(ctx, req)
	i.finish(ctx, rec, held, resp, handlerErr)
	return resp, handlerErr
}

// printableASCII accepts what a client puts into a header: UUIDs and the like.
// takeOver decides what a repeat of an existing key does: replay the outcome, run the
// call again under the record, or be refused. It returns the record to run under, or to
// replay with replay set, or the status for the caller.
func (i *idempotency) takeOver(ctx context.Context, rec IdempotencyRecord, fingerprint string, lease time.Time) (IdempotencyRecord, bool, error) {
	for attempt := 0; ; attempt++ {
		// The same key with another request is a client bug, not a repeat.
		if rec.Fingerprint != fingerprint {
			return rec, false, status.Error(codes.AlreadyExists, "duplicate request with different payload")
		}
		// A completed record past its retention is gone, even while DeleteExpired has not
		// removed it yet: the repeat runs the call again. A store that does not let it go
		// — Retake of a project's store written before — keeps the replay.
		expired := rec.Status == idemCompleted && i.now().After(rec.ExpiresAt)
		if rec.Status == idemCompleted && (!expired || attempt > 0) {
			return rec, true, nil
		}
		// A retry, a call whose lease expired or an expired outcome: one repeat takes it
		// over, the store decides which.
		took, err := i.store.Retake(ctx, rec.ID, lease)
		if err != nil {
			i.log.ErrorContext(ctx, "idempotency retake failed", "method", rec.Method, "err", err)
			return rec, false, status.Error(codes.Internal, "internal error")
		}
		if took {
			rec.Status, rec.ExpiresAt, rec.Lease = idemInProgress, lease, lease
			return rec, false, nil
		}
		if attempt > 0 || !expired {
			// A live call or a retry another repeat took: refused, as always.
			return rec, false, status.Error(codes.Aborted, "request is already in progress")
		}
		// Another repeat took the expired outcome first: read the record again and decide on what it is now —
		// in progress is Aborted, a fresh outcome is replayed. Begin reads it, and holds
		// the key itself when the record was deleted in between.
		var created bool
		rec, created, err = i.store.Begin(ctx, IdempotencyRecord{
			User: rec.User, Method: rec.Method, Key: rec.Key, Fingerprint: fingerprint,
			Status: idemInProgress, ExpiresAt: lease,
		})
		if err != nil {
			i.log.ErrorContext(ctx, "idempotency check failed", "method", rec.Method, "err", err)
			return rec, false, status.Error(codes.Internal, "internal error")
		}
		if created {
			rec.Lease = rec.ExpiresAt
			return rec, false, nil
		}
	}
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// heldLease extends the lease of a running call every lease/3 until released.
type heldLease struct {
	mu    sync.Mutex
	lease time.Time
	// pending is the token of an extension whose outcome is unknown: it failed with
	// an error, a timeout say, and may still have reached the database. The holder
	// tries it when the current token no longer matches.
	pending time.Time
	stop    context.CancelFunc
	done    chan struct{}
	once    sync.Once
}

func (i *idempotency) hold(ctx context.Context, rec IdempotencyRecord) *heldLease {
	h := &heldLease{lease: rec.Lease, done: make(chan struct{})}
	ext, ok := i.store.(IdempotencyLeaseExtender)
	if !ok || i.lease/3 <= 0 {
		h.stop = func() {}
		close(h.done)
		return h
	}
	// The extension itself runs on a context the release does not cancel: an
	// extension that reached the database while being cancelled would leave the new
	// token unknown here, and Finish would take the lease for lost.
	base := context.WithoutCancel(ctx)
	stopCtx, stop := context.WithCancel(base)
	h.stop = stop
	go func() {
		defer close(h.done)
		ticker := time.NewTicker(i.lease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stopCtx.Done():
				return
			case <-ticker.C:
			}
			h.mu.Lock()
			cur, pending := h.lease, h.pending
			h.mu.Unlock()
			next := i.leaseUntil()
			kept, err := extend(ext, base, i.lease, rec.ID, cur, next)
			if err == nil && !kept && !pending.IsZero() {
				kept, err = extend(ext, base, i.lease, rec.ID, pending, next)
			}
			switch {
			case err != nil:
				// A passing database failure: the lease is still alive, try on the next
				// tick, with next as a candidate in case the write went through.
				h.mu.Lock()
				h.pending = next
				h.mu.Unlock()
				i.log.WarnContext(ctx, "idempotency lease was not extended", "method", rec.Method, "err", err)
			case !kept:
				i.log.WarnContext(ctx, "idempotency lease was taken over while the call ran", "method", rec.Method)
				return
			default:
				h.mu.Lock()
				h.lease, h.pending = next, time.Time{}
				h.mu.Unlock()
			}
		}
	}()
	return h
}

func extend(ext IdempotencyLeaseExtender, base context.Context, lease time.Duration, id int64, from, to time.Time) (bool, error) {
	ctx, cancel := context.WithTimeout(base, min(lease/3, leaseExtendTimeout))
	defer cancel()
	return ext.ExtendLease(ctx, id, from, to)
}

// release stops the extension, waits for one in flight and returns the current lease
// and the token of an extension whose outcome is unknown.
func (h *heldLease) release() (lease, pending time.Time) {
	h.once.Do(h.stop)
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lease, h.pending
}

// finishAttempts is how many times a store failure of the final write is retried within
// idempotencySaveTimeout: a lost write leaves the key in progress, and a repeat after the
// lease would run the call again.
const finishAttempts = 3

func (i *idempotency) finish(ctx context.Context, rec IdempotencyRecord, held *heldLease, resp any, handlerErr error) {
	lease, pending := held.release()
	rec.Lease = lease
	now := i.now()
	rec.Code, rec.Message, rec.Response = 0, "", nil
	keep := true
	if handlerErr == nil {
		m, ok := resp.(proto.Message)
		var err error
		if ok {
			rec.Response, err = proto.Marshal(m)
		}
		// A success that cannot be kept must not be remembered as an empty answer.
		keep = ok && err == nil
	} else {
		// The client got the public form of the error, and a repeat must get the same,
		// details included.
		st := publicStatus(handlerErr)
		rec.Code, rec.Message = int32(st.Code()), st.Message() // #nosec G115 -- gRPC codes are small
		body, err := proto.Marshal(st.Proto())
		if err != nil {
			body, _ = proto.Marshal(&spb.Status{Code: rec.Code, Message: rec.Message})
		}
		rec.Response = body
		keep = cachedCodes[st.Code()]
		var marked RetryableError
		if errors.As(handlerErr, &marked) {
			keep = !marked.IsRetryable()
		}
	}
	result := idemStored
	rec.Status, rec.ExpiresAt = idemCompleted, now.Add(i.retention)
	if !keep {
		result = idemRetried
		rec.Status, rec.ExpiresAt = idemRetry, now.Add(i.lease)
	}

	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), idempotencySaveTimeout)
	defer cancel()
	var err error
	for attempt := 1; ; attempt++ {
		err = i.store.Finish(saveCtx, rec)
		if errors.Is(err, ErrIdempotencyLeaseLost) && !pending.IsZero() {
			// The last extension may have gone through after all.
			rec.Lease, pending = pending, time.Time{}
			err = i.store.Finish(saveCtx, rec)
		}
		if err == nil || errors.Is(err, ErrIdempotencyLeaseLost) || attempt == finishAttempts || saveCtx.Err() != nil {
			break
		}
		select {
		case <-saveCtx.Done():
		case <-time.After(time.Duration(attempt) * 100 * time.Millisecond):
		}
	}
	switch {
	case errors.Is(err, ErrIdempotencyLeaseLost):
		i.count(rec.Method, idemLost)
		i.log.WarnContext(ctx, "idempotency key was taken over, the outcome is not saved", "method", rec.Method)
	case err != nil:
		i.count(rec.Method, idemSaveFailed)
		i.log.ErrorContext(ctx, "idempotency outcome was not saved", "method", rec.Method, "err", err)
	default:
		i.count(rec.Method, result)
	}
}

func (i *idempotency) cached(ctx context.Context, method string, rec IdempotencyRecord) (any, error) {
	if code := codes.Code(rec.Code); code != codes.OK {
		if len(rec.Response) > 0 {
			var st spb.Status
			if err := proto.Unmarshal(rec.Response, &st); err == nil && st.GetCode() == rec.Code {
				return nil, status.FromProto(&st).Err()
			}
		}
		return nil, status.Error(code, rec.Message)
	}
	msg, err := responseType(method)
	if err == nil {
		err = proto.Unmarshal(rec.Response, msg)
	}
	if err != nil {
		i.log.ErrorContext(ctx, "a remembered idempotency outcome cannot be read", "method", method, "err", err)
		return nil, status.Error(codes.Internal, "internal error")
	}
	return msg, nil
}

// publicStatus is the status a client receives for an error: what errorsUnary makes of it.
func publicStatus(err error) *status.Status {
	if st, ok := status.FromError(err); ok {
		return st
	}
	switch {
	case errors.Is(err, context.Canceled):
		return status.New(codes.Canceled, "the call was canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.New(codes.DeadlineExceeded, "the call took too long")
	}
	return status.New(codes.Internal, "internal error")
}

// mutating tells whether a method changes something, for the default list of methods
// whose keys count: its HTTP binding is not a GET and its idempotency_level is not
// NO_SIDE_EFFECTS. A method the proto registry does not know counts as mutating.
func mutating(fullMethod string) bool {
	md, err := methodDescriptor(fullMethod)
	if err != nil {
		return true
	}
	opts, ok := md.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil {
		return true
	}
	if opts.GetIdempotencyLevel() == descriptorpb.MethodOptions_NO_SIDE_EFFECTS {
		return false
	}
	if rule, ok := proto.GetExtension(opts, annotations.E_Http).(*annotations.HttpRule); ok && rule != nil && rule.GetGet() != "" {
		return false
	}
	return true
}

func idempotencyKey(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if v := md.Get(strings.ToLower(IdempotencyKeyHeader)); len(v) > 0 {
		return strings.TrimSpace(v[0])
	}
	return ""
}

func fingerprintOf(msg proto.Message) (string, error) {
	data, err := proto.MarshalOptions{Deterministic: true}.Marshal(msg)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// methodDescriptor finds a method in the proto registry.
func methodDescriptor(fullMethod string) (protoreflect.MethodDescriptor, error) {
	service, method, ok := strings.Cut(strings.TrimPrefix(fullMethod, "/"), "/")
	if !ok {
		return nil, fmt.Errorf("bad method %q", fullMethod)
	}
	desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(service))
	if err != nil {
		return nil, err
	}
	sd, ok := desc.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, fmt.Errorf("%s is not a service", service)
	}
	md := sd.Methods().ByName(protoreflect.Name(method))
	if md == nil {
		return nil, fmt.Errorf("no method %s", fullMethod)
	}
	return md, nil
}

// responseType finds the response message of a method in the proto registry.
func responseType(fullMethod string) (proto.Message, error) {
	md, err := methodDescriptor(fullMethod)
	if err != nil {
		return nil, err
	}
	mt, err := protoregistry.GlobalTypes.FindMessageByName(md.Output().FullName())
	if err != nil {
		return nil, err
	}
	return mt.New().Interface(), nil
}

// MemoryIdempotencyStore keeps records in memory. It does not survive a restart and is
// not shared between instances: it is for tests.
type MemoryIdempotencyStore struct {
	mu      sync.Mutex
	records map[string]*IdempotencyRecord
	nextID  int64
}

// NewMemoryIdempotencyStore creates an empty store.
func NewMemoryIdempotencyStore() *MemoryIdempotencyStore {
	return &MemoryIdempotencyStore{records: map[string]*IdempotencyRecord{}}
}

func memKey(r IdempotencyRecord) string { return r.User + "\x00" + r.Method + "\x00" + r.Key }

func (s *MemoryIdempotencyStore) Begin(_ context.Context, rec IdempotencyRecord) (IdempotencyRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[memKey(rec)]; ok {
		return *existing, false, nil
	}
	s.nextID++
	rec.ID = s.nextID
	s.records[memKey(rec)] = &rec
	return rec, true, nil
}

func (s *MemoryIdempotencyStore) Retake(_ context.Context, id int64, lockUntil time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID(id)
	if r == nil {
		return false, nil
	}
	if r.Status == idemRetry || ((r.Status == idemInProgress || r.Status == idemCompleted) && time.Now().After(r.ExpiresAt)) {
		r.Status, r.ExpiresAt = idemInProgress, lockUntil
		return true, nil
	}
	return false, nil
}

// ExtendLease implements IdempotencyLeaseExtender.
func (s *MemoryIdempotencyStore) ExtendLease(_ context.Context, id int64, lease, until time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID(id)
	if r == nil || r.Status != idemInProgress || !r.ExpiresAt.Equal(lease) {
		return false, nil
	}
	r.ExpiresAt = until
	return true, nil
}

func (s *MemoryIdempotencyStore) Finish(_ context.Context, rec IdempotencyRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.byID(rec.ID)
	if r == nil {
		if rec.Lease.IsZero() {
			return nil
		}
		return ErrIdempotencyLeaseLost
	}
	if !rec.Lease.IsZero() && (r.Status != idemInProgress || !r.ExpiresAt.Equal(rec.Lease)) {
		return ErrIdempotencyLeaseLost
	}
	rec.Lease = time.Time{}
	*r = rec
	return nil
}

func (s *MemoryIdempotencyStore) byID(id int64) *IdempotencyRecord {
	for _, r := range s.records {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func (s *MemoryIdempotencyStore) DeleteExpired(_ context.Context, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, r := range s.records {
		if r.ExpiresAt.Before(now) {
			delete(s.records, k)
			n++
		}
	}
	return n, nil
}

// pgIdempotencyStore keeps records in PostgreSQL, shared by every instance.
type pgIdempotencyStore struct{ pool *pgxpool.Pool }

const idempotencyTable = "platform_idempotency_keys"

// NewPostgresIdempotencyStore creates the table if needed and returns the store the module
// uses with the postgres module.
func NewPostgresIdempotencyStore(ctx context.Context, pool *pgxpool.Pool) (IdempotencyStore, error) {
	return newPGIdempotencyStore(ctx, pool)
}

// idempotencySchema are the versions of the idempotency table. Version 1 is the schema
// releases before versioning created, so it keeps IF NOT EXISTS; later steps only append.
var idempotencySchema = []pgdb.SchemaStep{{Version: 1, Statements: []string{
	`CREATE TABLE IF NOT EXISTS ` + idempotencyTable + ` (
	id          bigserial   PRIMARY KEY,
	user_id     text        NOT NULL,
	method      text        NOT NULL,
	key         text        NOT NULL,
	fingerprint text        NOT NULL,
	status      text        NOT NULL,
	code        integer     NOT NULL DEFAULT 0,
	message     text        NOT NULL DEFAULT '',
	response    bytea,
	created_at  timestamptz NOT NULL DEFAULT now(),
	updated_at  timestamptz NOT NULL DEFAULT now(),
	expires_at  timestamptz NOT NULL,
	UNIQUE (user_id, method, key)
)`,
	`CREATE INDEX IF NOT EXISTS ` + idempotencyTable + `_expires_at_idx ON ` + idempotencyTable + ` (expires_at)`,
}}}

func newPGIdempotencyStore(ctx context.Context, pool *pgxpool.Pool) (*pgIdempotencyStore, error) {
	if err := pgdb.MigrateSchema(ctx, pool, "api:idempotency", idempotencySchema); err != nil {
		return nil, fmt.Errorf("api: idempotency table: %w", err)
	}
	return &pgIdempotencyStore{pool: pool}, nil
}

const idemColumns = "id, user_id, method, key, fingerprint, status, code, message, response, expires_at"

func scanIdem(row pgx.Row) (IdempotencyRecord, error) {
	var r IdempotencyRecord
	err := row.Scan(&r.ID, &r.User, &r.Method, &r.Key, &r.Fingerprint, &r.Status, &r.Code, &r.Message, &r.Response, &r.ExpiresAt)
	return r, err
}

func (s *pgIdempotencyStore) Begin(ctx context.Context, rec IdempotencyRecord) (IdempotencyRecord, bool, error) {
	created, err := scanIdem(s.pool.QueryRow(ctx,
		"INSERT INTO "+idempotencyTable+" (user_id, method, key, fingerprint, status, expires_at) VALUES ($1, $2, $3, $4, $5, $6) "+
			"ON CONFLICT (user_id, method, key) DO NOTHING RETURNING "+idemColumns,
		rec.User, rec.Method, rec.Key, rec.Fingerprint, rec.Status, rec.ExpiresAt))
	if err == nil {
		return created, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return IdempotencyRecord{}, false, err
	}
	existing, err := scanIdem(s.pool.QueryRow(ctx,
		"SELECT "+idemColumns+" FROM "+idempotencyTable+" WHERE user_id = $1 AND method = $2 AND key = $3",
		rec.User, rec.Method, rec.Key))
	return existing, false, err
}

// Retake is a compare and set: of two repeats racing for a retry, an expired lease or
// an expired outcome, one wins.
func (s *pgIdempotencyStore) Retake(ctx context.Context, id int64, lockUntil time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		"UPDATE "+idempotencyTable+" SET status = 'in_progress', expires_at = $2, updated_at = now() "+
			"WHERE id = $1 AND (status = 'retry' OR (status IN ('in_progress', 'completed') AND expires_at < now()))",
		id, lockUntil)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ExtendLease implements IdempotencyLeaseExtender: the expiry moves only while it is
// still the lease the call holds.
func (s *pgIdempotencyStore) ExtendLease(ctx context.Context, id int64, lease, until time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		"UPDATE "+idempotencyTable+" SET expires_at = $3, updated_at = now() "+
			"WHERE id = $1 AND status = 'in_progress' AND expires_at = $2",
		id, lease, until)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Finish writes the outcome; with a lease, only while the record still holds it, so a
// call whose key was taken over does not overwrite the outcome of the one that took it.
func (s *pgIdempotencyStore) Finish(ctx context.Context, rec IdempotencyRecord) error {
	query := "UPDATE " + idempotencyTable + " SET status = $2, code = $3, message = $4, response = $5, expires_at = $6, updated_at = now() WHERE id = $1"
	args := []any{rec.ID, rec.Status, rec.Code, rec.Message, rec.Response, rec.ExpiresAt}
	if !rec.Lease.IsZero() {
		query += " AND status = 'in_progress' AND expires_at = $7"
		args = append(args, rec.Lease)
	}
	tag, err := s.pool.Exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if !rec.Lease.IsZero() && tag.RowsAffected() == 0 {
		return ErrIdempotencyLeaseLost
	}
	return nil
}

func (s *pgIdempotencyStore) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	// An in_progress record past its lease belongs to a call that died — a running one
	// keeps extending it — so it goes too.
	tag, err := s.pool.Exec(ctx, "DELETE FROM "+idempotencyTable+" WHERE expires_at < $1", now)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
