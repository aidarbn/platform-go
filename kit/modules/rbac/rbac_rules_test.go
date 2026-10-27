package rbac_test

import (
	"testing"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/modules/rbac"
)

func TestLoadAndRules(t *testing.T) {
	l := confx.New("")
	_ = rbac.Load(l)
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
	e, err := rbac.NewEnforcer(policy)
	if err != nil {
		t.Fatal(err)
	}
	rules := e.Rules()
	if len(rules) != 4 || rules[0].Method != "/grpc.health.v1.Health/*" {
		t.Fatalf("rules = %+v", rules)
	}
	// The rules are a copy: changing them does not change the policy.
	rules[0].Role = "nobody"
	if e.Rules()[0].Role != "*" {
		t.Fatal("Rules returned the enforcer's own slice")
	}
}
