package rbac

import (
	"bufio"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/persist"
)

// textAdapter loads a policy text in casbin's CSV format into a model of the project's
// own, reporting a broken rule with its line, and remembers the line of every rule.
// Changes made through the casbin API stay in memory: the text is part of the binary.
type textAdapter struct {
	text  string
	mu    sync.Mutex
	lines map[string]int
}

func ruleKey(ptype string, rule []string) string {
	return ptype + "\x00" + strings.Join(rule, "\x00")
}

func (a *textAdapter) LoadPolicy(m model.Model) error {
	var errs []error
	lines := map[string]int{}
	scanner := bufio.NewScanner(strings.NewReader(a.text))
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := persist.LoadPolicyLine(line, m); err != nil {
			errs = append(errs, fmt.Errorf("policy line %d: %w", n, err))
			continue
		}
		fields := strings.Split(line, ",")
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		if _, ok := lines[ruleKey(fields[0], fields[1:])]; !ok {
			lines[ruleKey(fields[0], fields[1:])] = n
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	a.mu.Lock()
	a.lines = lines
	a.mu.Unlock()
	return nil
}

// line is the line of a rule in the text, 0 for a rule added through the casbin API.
func (a *textAdapter) line(ptype string, rule []string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lines[ruleKey(ptype, rule)]
}

func (a *textAdapter) SavePolicy(model.Model) error                              { return nil }
func (a *textAdapter) AddPolicy(string, string, []string) error                  { return nil }
func (a *textAdapter) RemovePolicy(string, string, []string) error               { return nil }
func (a *textAdapter) AddPolicies(string, string, [][]string) error              { return nil }
func (a *textAdapter) RemovePolicies(string, string, [][]string) error           { return nil }
func (a *textAdapter) RemoveFilteredPolicy(string, string, int, ...string) error { return nil }
func (a *textAdapter) UpdatePolicy(string, string, []string, []string) error     { return nil }
func (a *textAdapter) UpdatePolicies(string, string, [][]string, [][]string) error {
	return nil
}
func (a *textAdapter) UpdateFilteredPolicies(string, string, [][]string, int, ...string) ([][]string, error) {
	return nil, nil
}

// checkedAdapter is the project's adapter in taply's model: every load is checked as
// ParsePolicy checks a text, so a broken rule fails the start or the reload and the
// policy in force stays. Writes go to the project's adapter.
type checkedAdapter struct {
	persist.Adapter
}

func (a *checkedAdapter) LoadPolicy(m model.Model) error {
	if err := a.Adapter.LoadPolicy(m); err != nil {
		return err
	}
	var errs []error
	for _, p := range m["p"]["p"].Policy {
		switch {
		case len(p) != 3 || p[0] == "" || p[1] == "" || p[2] == "":
			errs = append(errs, fmt.Errorf("policy rule %q: role, method and action must not be empty", strings.Join(p, ", ")))
		case !strings.HasPrefix(p[1], "/"):
			errs = append(errs, fmt.Errorf("policy rule %q: method %q must start with /, as in /shop.v1.OrdersService/*", strings.Join(p, ", "), p[1]))
		}
	}
	return errors.Join(errs...)
}

func (a *checkedAdapter) AddPolicies(sec, ptype string, rules [][]string) error {
	if b, ok := a.Adapter.(persist.BatchAdapter); ok {
		return b.AddPolicies(sec, ptype, rules)
	}
	for _, r := range rules {
		if err := a.Adapter.AddPolicy(sec, ptype, r); err != nil {
			return err
		}
	}
	return nil
}

func (a *checkedAdapter) RemovePolicies(sec, ptype string, rules [][]string) error {
	if b, ok := a.Adapter.(persist.BatchAdapter); ok {
		return b.RemovePolicies(sec, ptype, rules)
	}
	for _, r := range rules {
		if err := a.Adapter.RemovePolicy(sec, ptype, r); err != nil {
			return err
		}
	}
	return nil
}

var errNotUpdatable = errors.New("rbac: the adapter cannot update rules")

func (a *checkedAdapter) UpdatePolicy(sec, ptype string, oldRule, newRule []string) error {
	if u, ok := a.Adapter.(persist.UpdatableAdapter); ok {
		return u.UpdatePolicy(sec, ptype, oldRule, newRule)
	}
	return errNotUpdatable
}

func (a *checkedAdapter) UpdatePolicies(sec, ptype string, oldRules, newRules [][]string) error {
	if u, ok := a.Adapter.(persist.UpdatableAdapter); ok {
		return u.UpdatePolicies(sec, ptype, oldRules, newRules)
	}
	return errNotUpdatable
}

func (a *checkedAdapter) UpdateFilteredPolicies(sec, ptype string, newRules [][]string, fieldIndex int, fieldValues ...string) ([][]string, error) {
	if u, ok := a.Adapter.(persist.UpdatableAdapter); ok {
		return u.UpdateFilteredPolicies(sec, ptype, newRules, fieldIndex, fieldValues...)
	}
	return nil, errNotUpdatable
}
