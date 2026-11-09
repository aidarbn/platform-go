// Package ptrx has helpers for optional values held as pointers, ported from taply's
// ptr package: defaults, fallbacks, comparison by value and mapping to proto optional
// fields.
package ptrx

// ValueOr returns the dereferenced pointer value or the provided default when the pointer is nil.
func ValueOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// Prefer returns primary if non-nil, otherwise fallback. Used to migrate from a deprecated
// pointer field to a replacement while keeping backwards-compatible input handling.
func Prefer[T any](primary, fallback *T) *T {
	if primary != nil {
		return primary
	}
	return fallback
}

// Equal compares two optional values, nil meaning unset. Two nils are equal, a nil and a
// value are not, and two values are compared by content, not by address: this is what is
// meant by asking whether an optional field changed.
func Equal[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// NonZero returns &v when v differs from its zero value, and nil otherwise.
// Intended for mapping domain primitives to proto `optional` fields so that
// empty-string or zero-int values serialize as absent rather than as "" / 0.
func NonZero[T comparable](v T) *T {
	var zero T
	if v == zero {
		return nil
	}
	return &v
}
