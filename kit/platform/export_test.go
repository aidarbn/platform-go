package platform

import (
	"io"
	"testing"

	"github.com/aidarbn/platform-go/kit/logx"
)

// NewForTest создаёт контейнер без запуска модулей — для тестов пакета и его клиентов.
func NewForTest(tb testing.TB) *App {
	tb.Helper()
	return newApp(logx.New(logx.Options{Writer: io.Discard}))
}
