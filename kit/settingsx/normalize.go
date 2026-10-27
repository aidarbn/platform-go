package settingsx

import (
	"strconv"
	"strings"
	"time"
)

// Normalize returns the canonical text of a value of the setting, so that two spellings
// of the same value compare equal: "1m" and "60s" give "1m0s", "t" and "TRUE" give
// "true", "05" gives "5", "1.50" gives "1.5". A string keeps its text as is; a schedule
// loses repeated spaces and the case of a shorthand.
//
// Normalize checks only that the text parses as the kind; bounds and options are the
// business of Validate. The result is meant for comparison, not for storage: an
// administrator who wrote "1m" expects to see "1m".
func Normalize(def Definition, raw string) (string, error) {
	switch def.Kind {
	case KindBool:
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return "", parseScalar(def.Kind, raw)
		}
		return strconv.FormatBool(v), nil
	case KindInt, KindInt64:
		bits := 64
		if def.Kind == KindInt {
			bits = 0 // the size of int, the same as Atoi in validation
		}
		v, err := strconv.ParseInt(raw, 10, bits)
		if err != nil {
			return "", parseScalar(def.Kind, raw)
		}
		return strconv.FormatInt(v, 10), nil
	case KindFloat:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return "", parseScalar(def.Kind, raw)
		}
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	case KindDuration:
		v, err := time.ParseDuration(raw)
		if err != nil {
			return "", parseScalar(def.Kind, raw)
		}
		return v.String(), nil
	case KindString:
		return raw, nil
	case KindCron:
		if err := validateCron(raw); err != nil {
			return "", err
		}
		return normalizeCron(raw), nil
	default:
		return "", validateValue(def, raw)
	}
}

// normalizeCron expects a schedule validateCron accepted.
func normalizeCron(raw string) string {
	expr := strings.Join(strings.Fields(raw), " ")
	if !strings.HasPrefix(expr, "@") {
		return expr
	}
	if rest, ok := cutPrefixFold(expr, "@every "); ok {
		d, _ := time.ParseDuration(rest)
		return "@every " + d.String()
	}
	return strings.ToLower(expr)
}

// Equal reports whether two values of the setting mean the same thing. Values that do
// not parse are compared as text, so a broken value equals only itself.
func Equal(def Definition, a, b string) bool {
	na, errA := Normalize(def, a)
	nb, errB := Normalize(def, b)
	if errA != nil || errB != nil {
		return a == b
	}
	return na == nb
}
