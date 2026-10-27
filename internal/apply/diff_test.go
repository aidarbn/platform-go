package apply_test

import (
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/apply"
)

func TestUnifiedDiff(t *testing.T) {
	a := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	b := "a\nB\nc\nd\ne\nf\ng\nh\ni\nj\nk\n"
	got := apply.UnifiedDiff("x", "y", []byte(a), []byte(b))
	want := `--- x
+++ y
@@ -1,5 +1,5 @@
 a
-b
+B
 c
 d
 e
@@ -8,3 +8,4 @@
 h
 i
 j
+k
`
	if got != want {
		t.Errorf("diff:\n%s\nwant:\n%s", got, want)
	}
	if d := apply.UnifiedDiff("x", "y", nil, []byte("new\n")); d != "--- x\n+++ y\n@@ -0,0 +1 @@\n+new\n" {
		t.Errorf("from nothing:\n%s", d)
	}
	if d := apply.UnifiedDiff("x", "y", []byte(a), []byte(a)); d != "--- x\n+++ y\n" {
		t.Errorf("same:\n%s", d)
	}
	big := strings.Repeat("line\n", 5000)
	if d := apply.UnifiedDiff("x", "y", []byte(big), []byte(big+"z\n")); !strings.Contains(d, "too large") {
		t.Errorf("large:\n%s", d)
	}
}

func TestUnifiedDiffLastLineBreak(t *testing.T) {
	got := apply.UnifiedDiff("x", "y", []byte("a\nb"), []byte("a\nb\n"))
	want := "--- x\n+++ y\n@@ -1,2 +1,2 @@\n a\n-b\n\\ No newline at end of file\n+b\n"
	if got != want {
		t.Errorf("diff:\n%q\nwant:\n%q", got, want)
	}
}
