package enums_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/enumx"
	"github.com/aidarbn/platform-go/kit/i18nx"
	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/modules/enums"
	"github.com/aidarbn/platform-go/kit/platform"
)

var catalog = enumx.Catalog{
	{Entity: "order", Description: "Orders", Enums: []enumx.Enum{
		{Name: "status", Description: "Order status", Values: []enumx.Value{{Value: "new", Description: "just created"}, {Value: "paid", Description: "paid"}}},
	}},
}

func TestCatalog(t *testing.T) {
	if !catalog.Valid("order", "status", "paid") || catalog.Valid("order", "status", "lost") || catalog.Valid("order", "kind", "new") {
		t.Error("Valid is wrong")
	}
	if en, ok := catalog.Find("order", "status"); !ok || len(en.Values) != 2 {
		t.Errorf("Find = %+v, %v", en, ok)
	}
}

func TestTranslate(t *testing.T) {
	c := i18nx.New(nil, "ru", "ru", "kk")
	if err := c.LoadMessages([]byte("enum.order.status.new: {kk: жаңа}\nenum.order: {kk: Тапсырыстар}\n")); err != nil {
		t.Fatal(err)
	}
	got := enums.Translate(catalog, c, "kk")
	if got[0].Description != "Тапсырыстар" || got[0].Enums[0].Values[0].Description != "жаңа" || got[0].Enums[0].Values[1].Description != "paid" {
		t.Errorf("translated = %+v", got)
	}
	if catalog[0].Description != "Orders" {
		t.Error("the catalog itself was changed")
	}
}

func TestServesCatalog(t *testing.T) {
	apiModule := api.New(api.Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	modules := []platform.Module{apiModule, enums.New(enums.Config{}, enums.WithCatalog(catalog))}

	ctx, cancel := context.WithCancel(context.Background())
	started, errCh := make(chan string, 1), make(chan error, 1)
	cfg := platform.Config{Service: "enums", OpsAddr: "127.0.0.1:0", ShutdownTimeout: 3 * time.Second,
		Logger: logx.New(logx.Options{Writer: io.Discard}), OnStarted: func(a string) { started <- a }}
	var catalogFromApp enumx.Catalog
	wire := func(app *platform.App) error { catalogFromApp = enums.From(app); return nil }
	go func() { errCh <- platform.RunContext(ctx, cfg, modules, wire) }()
	select {
	case <-started:
	case err := <-errCh:
		t.Fatalf("Run: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("did not start")
	}
	defer func() { cancel(); <-errCh }()

	if !catalogFromApp.Valid("order", "status", "new") {
		t.Error("the catalog is not in the container")
	}
	resp, err := http.Get("http://" + apiModule.HTTPAddr() + "/v1/enums")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Entities enumx.Catalog `json:"entities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(body.Entities) != 1 || body.Entities[0].Enums[0].Values[0].Value != "new" {
		t.Errorf("GET /v1/enums = %d %+v", resp.StatusCode, body)
	}
}
