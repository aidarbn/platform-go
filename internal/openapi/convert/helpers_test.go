package convert_test

import (
	"slices"
	"strconv"
)

func sortedKeys(m map[string]int) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }
