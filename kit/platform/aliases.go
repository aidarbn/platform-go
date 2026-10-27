package platform

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/protobuf/proto"
)

var metricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// AliasMetric serves the metric name under alias as well, with the same series and
// values, for as long as a service moves to the platform: the dashboards and alerts
// written for its old metric keep working while they are moved to the platform's name.
// Remove the alias once nothing reads the old name.
//
//	app.AliasMetric("api_deprecated_calls_total", "maas_deprecated_rpc_calls_total")
//
// A metric may have several aliases. An alias is served only at /metrics and only while
// no real metric has the same name. A counter's alias ends in _total, like the counter.
func (a *App) AliasMetric(name, alias string) {
	for _, n := range []string{name, alias} {
		if !metricName.MatchString(n) {
			panic(fmt.Sprintf("platform: %q is not a metric name", n))
		}
	}
	if name == alias {
		panic(fmt.Sprintf("platform: metric %s cannot be its own alias", name))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for other, aliases := range a.aliases {
		if slices.Contains(aliases, alias) && other != name {
			panic(fmt.Sprintf("platform: %s is already an alias of %s", alias, other))
		}
	}
	if a.aliases == nil {
		a.aliases = map[string][]string{}
	}
	if !slices.Contains(a.aliases[name], alias) {
		a.aliases[name] = append(a.aliases[name], alias)
	}
}

// gatherer is what /metrics serves: the registry and the aliases of its metrics.
func (a *App) gatherer() prometheus.Gatherer {
	return prometheus.GathererFunc(func() ([]*dto.MetricFamily, error) {
		families, err := a.metrics.Gather()
		a.mu.RLock()
		defer a.mu.RUnlock()
		return withAliases(families, a.aliases), err
	})
}

// withAliases adds a copy of every aliased family under each of its aliases, keeping
// the families sorted by name as a Gatherer must.
func withAliases(families []*dto.MetricFamily, aliases map[string][]string) []*dto.MetricFamily {
	if len(aliases) == 0 {
		return families
	}
	real := make(map[string]bool, len(families))
	for _, f := range families {
		real[f.GetName()] = true
	}
	var copies []*dto.MetricFamily
	for _, f := range families {
		for _, alias := range aliases[f.GetName()] {
			if real[alias] {
				continue
			}
			c := proto.CloneOf(f)
			c.Name = proto.String(alias)
			c.Help = proto.String(strings.TrimSpace(f.GetHelp() + " (alias of " + f.GetName() + ")"))
			copies = append(copies, c)
		}
	}
	if len(copies) == 0 {
		return families
	}
	out := slices.Concat(families, copies)
	slices.SortFunc(out, func(x, y *dto.MetricFamily) int { return strings.Compare(x.GetName(), y.GetName()) })
	return out
}
