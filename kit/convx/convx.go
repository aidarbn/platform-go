// Package convx has generic helpers for converting slices, ported from taply's conv
// package.
package convx

// MapSlice converts a slice with mapFunc; a nil slice stays nil.
func MapSlice[From, To any](mapFunc func(From) To, from []From) []To {
	if from == nil {
		return nil
	}
	to := make([]To, len(from))
	for i, v := range from {
		to[i] = mapFunc(v)
	}
	return to
}

// MapSliceWithError converts a slice with mapFunc and stops at the first error; a nil
// slice stays nil.
func MapSliceWithError[From, To any](mapFunc func(From) (To, error), from []From) ([]To, error) {
	if from == nil {
		return nil, nil
	}
	to := make([]To, len(from))
	for i, v := range from {
		mapped, err := mapFunc(v)
		if err != nil {
			return nil, err
		}
		to[i] = mapped
	}
	return to, nil
}
