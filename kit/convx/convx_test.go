package convx_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/aidarbn/platform-go/kit/convx"
)

func double(x int) int { return x * 2 }

func TestMapSlice(t *testing.T) {
	t.Parallel()

	if got := convx.MapSlice(double, []int{1, 2, 3}); !reflect.DeepEqual(got, []int{2, 4, 6}) {
		t.Errorf("got %v", got)
	}
	if got := convx.MapSlice(double, []int{}); got == nil || len(got) != 0 {
		t.Errorf("an empty slice stays empty and non-nil, got %#v", got)
	}
	if got := convx.MapSlice(double, nil); got != nil {
		t.Errorf("a nil slice stays nil, got %#v", got)
	}
	if got := convx.MapSlice(double, []int{5}); !reflect.DeepEqual(got, []int{10}) {
		t.Errorf("got %v", got)
	}
}

func TestMapSliceWithError(t *testing.T) {
	t.Parallel()

	got, err := convx.MapSliceWithError(func(x int) (int, error) { return x * 2, nil }, []int{1, 2, 3})
	if err != nil || !reflect.DeepEqual(got, []int{2, 4, 6}) {
		t.Errorf("got %v, %v", got, err)
	}

	got, err = convx.MapSliceWithError(func(x int) (int, error) { return x, nil }, nil)
	if err != nil || got != nil {
		t.Errorf("a nil slice stays nil, got %#v, %v", got, err)
	}

	boom := errors.New("boom")
	got, err = convx.MapSliceWithError(func(x int) (int, error) {
		if x == 2 {
			return 0, boom
		}
		return x, nil
	}, []int{1, 2, 3})
	if !errors.Is(err, boom) || got != nil {
		t.Errorf("got %v, %v", got, err)
	}
}
