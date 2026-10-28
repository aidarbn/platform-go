package i18n_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/aidarbn/platform-go/kit/i18nx"
	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/modules/i18n"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/platform"
)

// cashRegister answers Fail with the timeout of the cash register, as taply's zebra does.
type cashRegister struct{ echo }

func (cashRegister) Fail(context.Context, *testv1.FailRequest) (*testv1.FailResponse, error) {
	return nil, api.ExternalTimeout("zebra", "external service timeout")
}

const cashRegisterMessages = `"error.msg.zebra: external service timeout": {en: "The cash register did not respond in time. Please try again", ru: "Касса не ответила вовремя. Попробуйте ещё раз", kk: "Касса уақытында жауап бермеді. Қайталап көріңіз"}
`

// WithExternalErrors gives the guest the translated timeout of the provider over REST,
// still a 504 with its ErrorInfo; without the option the text names the provider.
func TestExternalErrors(t *testing.T) {
	dsn := database(t)
	for _, tt := range []struct {
		name string
		opts []i18n.Option
		want map[string]string
	}{
		{"off", nil, map[string]string{
			"ru": "zebra: external service timeout",
			"kk": "zebra: external service timeout",
		}},
		{"on", []i18n.Option{i18n.WithExternalErrors()}, map[string]string{
			"ru": "Касса не ответила вовремя. Попробуйте ещё раз",
			"kk": "Касса уақытында жауап бермеді. Қайталап көріңіз",
			"en": "The cash register did not respond in time. Please try again",
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			apiModule := api.New(api.Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
			modules := []platform.Module{
				postgres.New(postgres.Config{URL: dsn}),
				apiModule,
				i18n.New(i18n.Config{DefaultLocale: "ru", Locales: []i18nx.Locale{"ru", "kk", "en"}}, i18n.WithMessages(cashRegisterMessages)),
			}
			if err := i18n.Configure(modules, tt.opts...); err != nil {
				t.Fatal(err)
			}
			wire := func(a *platform.App) error {
				api.Register(a, api.Service{
					GRPC:    func(s *grpc.Server) { testv1.RegisterEchoServiceServer(s, cashRegister{}) },
					Gateway: testv1.RegisterEchoServiceHandler,
				})
				return nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			started, errCh := make(chan string, 1), make(chan error, 1)
			cfg := platform.Config{
				Service: "i18n-external-test", OpsAddr: "127.0.0.1:0", ShutdownTimeout: 3 * time.Second,
				Logger: logx.New(logx.Options{Writer: io.Discard}), OnStarted: func(a string) { started <- a },
			}
			go func() { errCh <- platform.RunContext(ctx, cfg, modules, wire) }()
			select {
			case <-started:
			case err := <-errCh:
				t.Fatalf("Run: %v", err)
			case <-time.After(20 * time.Second):
				t.Fatal("did not start")
			}
			defer func() {
				cancel()
				<-errCh
			}()

			for locale, want := range tt.want {
				req, _ := http.NewRequest("GET", "http://"+apiModule.HTTPAddr()+"/v1/fail/none", nil)
				req.Header.Set("Accept-Language", locale)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				var body struct {
					Message string `json:"message"`
					Details []struct {
						Reason   string            `json:"reason"`
						Metadata map[string]string `json:"metadata"`
					} `json:"details"`
				}
				if err := json.Unmarshal(raw, &body); err != nil {
					t.Fatalf("%s: %v %s", locale, err, raw)
				}
				if resp.StatusCode != http.StatusGatewayTimeout || body.Message != want {
					t.Errorf("%s: %d %q", locale, resp.StatusCode, body.Message)
				}
				if len(body.Details) != 1 || body.Details[0].Reason != api.ReasonExternalTimeout || body.Details[0].Metadata["provider"] != "zebra" {
					t.Errorf("%s: details %s", locale, raw)
				}
			}
		})
	}
}
