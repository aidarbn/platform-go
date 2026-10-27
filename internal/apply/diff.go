package apply

import (
	"fmt"
	"strings"
)

// maxDiffCells bounds the work of a diff: two files of a few thousand lines each.
const maxDiffCells = 16_000_000

// UnifiedDiff is a line diff of a and b in the unified format with three lines of
// context, for people reading a conflict. Files too large to compare cheaply get a one
// line summary instead.
func UnifiedDiff(aName, bName string, a, b []byte) string {
	x, y := lines(a), lines(b)
	if len(x)*len(y) > maxDiffCells {
		return fmt.Sprintf("--- %s\n+++ %s\n(%d lines against %d, too large to show)\n", aName, bName, len(x), len(y))
	}
	ops := diffLines(x, y)

	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", aName, bName)
	const context = 3
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		// A hunk: changes and the context around them, merging changes closer than
		// twice the context.
		start := max(0, i-context)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*context {
				end = min(len(ops), end+context)
				break
			}
			end = run
		}
		aStart, bStart, aLen, bLen := ops[start].a, ops[start].b, 0, 0
		for _, op := range ops[start:end] {
			if op.kind != '+' {
				aLen++
			}
			if op.kind != '-' {
				bLen++
			}
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", hunkRange(aStart, aLen), hunkRange(bStart, bLen))
		for _, op := range ops[start:end] {
			out.WriteByte(op.kind)
			out.WriteString(op.line)
			out.WriteByte('\n')
		}
		i = end
	}
	return out.String()
}

func hunkRange(start, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if n == 1 {
		return fmt.Sprint(start + 1)
	}
	return fmt.Sprintf("%d,%d", start+1, n)
}

type diffOp struct {
	kind byte // ' ', '-' or '+'
	line string
	a, b int // index of the line in a and in b where the op starts
}

// noNewline marks a last line without a line break, so it differs from the same text
// with one; it is printed as diff prints it.
const noNewline = "\n\\ No newline at end of file"

func lines(content []byte) []string {
	s := string(content)
	if s == "" {
		return nil
	}
	missing := !strings.HasSuffix(s, "\n")
	out := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if missing {
		out[len(out)-1] += noNewline
	}
	return out
}

// diffLines is the longest common subsequence of the lines, as a list of operations.
func diffLines(x, y []string) []diffOp {
	n, m := len(x), len(y)
	// lcs[i][j] is the length of the LCS of x[i:] and y[j:].
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && x[i] == y[j]:
			ops = append(ops, diffOp{' ', x[i], i, j})
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			ops = append(ops, diffOp{'-', x[i], i, j})
			i++
		default:
			ops = append(ops, diffOp{'+', y[j], i, j})
			j++
		}
	}
	return ops
}
