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
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// IdempotencyKeyHeader is the header a client sends to make a call safe to repeat.
const IdempotencyKeyHeader = "Idempotency-Key"

// Statuses of an idempotency record, as in taply.
const (
	idemInProgress = "in_progress"
	idemCompleted  = "completed"
	idemRetry      = "retry"
)

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
	Response    []byte
	ExpiresAt   time.Time
}

// IdempotencyStore keeps the records. The postgres store is used when the postgres
// module is enabled; tests use the memory one.
type IdempotencyStore interface {
	// Begin inserts an in_progress record, or returns the existing one with created false.
	Begin(ctx context.Context, rec IdempotencyRecord) (IdempotencyRecord, bool, error)
	// Retake moves a retry record, or an in_progress one whose lock expired, back to
	// in_progress. It reports false when another call took it first.
	Retake(ctx context.Context, id int64, lockUntil time.Time) (bool, error)
	// Finish stores the outcome.
	Finish(ctx context.Context, rec IdempotencyRecord) error
	// DeleteExpired removes records past their expiry.
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
}

// RetryableError marks an error after which a repeated call must run again instead of
// getting the cached failure: a timeout of an external system, a lost connection.
type RetryableError interface{ IsRetryable() bool }

type idempotency struct {
	store     IdempotencyStore
	required  map[string]bool
	user      func(context.Context) string
	retention time.Duration
	lock      time.Duration
	log       *slog.Logger
	now       func() time.Time
}

func (i *idempotency) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	key := idempotencyKey(ctx)
	if key == "" {
		if i.required[info.FullMethod] {
			return nil, status.Errorf(codes.InvalidArgument, "%s is required in the header", IdempotencyKeyHeader)
		}
		return handler(ctx, req)
	}
	if len(key) > 255 {
		return nil, status.Errorf(codes.InvalidArgument, "%s is longer than 255 characters", IdempotencyKeyHeader)
	}

	msg, ok := req.(proto.Message)
	if !ok {
		return handler(ctx, req)
	}
	fingerprint, err := fingerprintOf(msg)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}

	user := ""
	if i.user != nil {
		user = i.user(ctx)
	}
	now := i.now()
	rec, created, err := i.store.Begin(ctx, IdempotencyRecord{
		User: user, Method: info.FullMethod, Key: key, Fingerprint: fingerprint,
		Status: idemInProgress, ExpiresAt: now.Add(i.lock),
	})
	if err != nil {
		i.log.ErrorContext(ctx, "idempotency check failed", "method", info.FullMethod, "err", err)
		return nil, status.Error(codes.Internal, "internal error")
	}

	if !created {
		// The same key with another request is a client bug, not a repeat.
		if rec.Fingerprint != fingerprint {
			return nil, status.Error(codes.AlreadyExists, "duplicate request with different payload")
		}
		switch {
		case rec.Status == idemCompleted:
			return i.cached(info.FullMethod, rec)
		case rec.Status == idemRetry || (rec.Status == idemInProgress && now.After(rec.ExpiresAt)):
			took, err := i.store.Retake(ctx, rec.ID, now.Add(i.lock))
			if err != nil {
				i.log.ErrorContext(ctx, "idempotency retake failed", "method", info.FullMethod, "err", err)
				return nil, status.Error(codes.Internal, "internal error")
			}
			if !took {
				return nil, status.Error(codes.Aborted, "request is already in progress")
			}
		default:
			return nil, status.Error(codes.Aborted, "request is already in progress")
		}
	}

	resp, handlerErr := handler(ctx, req)
	i.finish(ctx, rec, resp, handlerErr)
	return resp, handlerErr
}

func (i *idempotency) finish(ctx context.Context, rec IdempotencyRecord, resp any, handlerErr error) {
	now := i.now()
	rec.Status, rec.ExpiresAt = idemCompleted, now.Add(i.retention)
	rec.Code, rec.Message, rec.Response = 0, "", nil

	if handlerErr == nil {
		if m, ok := resp.(proto.Message); ok {
			rec.Response, _ = proto.Marshal(m)
		}
	} else {
		// The client got the public form of the error, and a repeat must get the same.
		st := publicStatus(handlerErr)
		rec.Code, rec.Message = int32(st.Code()), st.Message() // #nosec G115 -- gRPC codes are small
		if retryable(handlerErr) {
			rec.Status, rec.ExpiresAt = idemRetry, now.Add(i.lock)
		}
	}
	if err := i.store.Finish(context.WithoutCancel(ctx), rec); err != nil {
		i.log.ErrorContext(ctx, "idempotency result was not saved", "method", rec.Method, "err", err)
	}
}

func (i *idempotency) cached(method string, rec IdempotencyRecord) (any, error) {
	if code := codes.Code(rec.Code); code != codes.OK {
		return nil, status.Error(code, rec.Message)
	}
	msg, err := responseType(method)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}
	if err := proto.Unmarshal(rec.Response, msg); err != nil {
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

// retryable decides whether a failed call may run again. As in taply a failure is final
// unless marked; transport level failures that say "try again" count as marked.
func retryable(err error) bool {
	var marked RetryableError
	if errors.As(err, &marked) {
		return marked.IsRetryable()
	}
	switch publicStatus(err).Code() {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted:
		return true
	}
	return false
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

// responseType finds the response message of a method in the proto registry.
func responseType(fullMethod string) (proto.Message, error) {
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
	for _, r := range s.records {
		if r.ID != id {
			continue
		}
		if r.Status == idemRetry || (r.Status == idemInProgress && time.Now().After(r.ExpiresAt)) {
			r.Status, r.ExpiresAt = idemInProgress, lockUntil
			return true, nil
		}
		return false, nil
	}
	return false, nil
}

func (s *MemoryIdempotencyStore) Finish(_ context.Context, rec IdempotencyRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.records[memKey(rec)]; ok {
		*r = rec
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

func newPGIdempotencyStore(ctx context.Context, pool *pgxpool.Pool) (*pgIdempotencyStore, error) {
	for _, sql := range []string{
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
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			return nil, fmt.Errorf("api: idempotency table: %w", err)
		}
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

// Retake is a compare and set: of two repeats racing for a retry, one wins.
func (s *pgIdempotencyStore) Retake(ctx context.Context, id int64, lockUntil time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		"UPDATE "+idempotencyTable+" SET status = 'in_progress', expires_at = $2, updated_at = now() "+
			"WHERE id = $1 AND (status = 'retry' OR (status = 'in_progress' AND expires_at < now()))",
		id, lockUntil)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *pgIdempotencyStore) Finish(ctx context.Context, rec IdempotencyRecord) error {
	_, err := s.pool.Exec(ctx,
		"UPDATE "+idempotencyTable+" SET status = $2, code = $3, message = $4, response = $5, expires_at = $6, updated_at = now() WHERE id = $1",
		rec.ID, rec.Status, rec.Code, rec.Message, rec.Response, rec.ExpiresAt)
	return err
}

func (s *pgIdempotencyStore) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	// An in_progress record past its lock belongs to a call that died; it goes too.
	tag, err := s.pool.Exec(ctx, "DELETE FROM "+idempotencyTable+" WHERE expires_at < $1", now)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
