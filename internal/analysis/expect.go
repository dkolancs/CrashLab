package analysis

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Assertion is one reliability expectation from an experiment file.
type Assertion struct {
	Name  string
	Op    string
	Limit float64
	Raw   string
}

// AssertionResult is the pass or fail line for one expectation.
type AssertionResult struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
}

// ParseAssertion reads expressions such as "< 5%", "< 1000ms", and "< 10s".
func ParseAssertion(name, expr string) (Assertion, error) {
	fields := strings.Fields(expr)
	if len(fields) != 2 || fields[0] != "<" {
		return Assertion{}, fmt.Errorf("assertion %s: want an expression like \"< 5%%\"", name)
	}
	rawValue := fields[1]
	var limit float64
	switch name {
	case "error_rate":
		if !strings.HasSuffix(rawValue, "%") {
			return Assertion{}, fmt.Errorf("assertion %s: percent required", name)
		}
		n, err := strconv.ParseFloat(strings.TrimSuffix(rawValue, "%"), 64)
		if err != nil {
			return Assertion{}, err
		}
		limit = n / 100
	case "p95_latency":
		d, err := time.ParseDuration(rawValue)
		if err != nil {
			return Assertion{}, fmt.Errorf("assertion %s: %w", name, err)
		}
		limit = float64(d.Milliseconds())
	case "recovery_time":
		d, err := time.ParseDuration(rawValue)
		if err != nil {
			return Assertion{}, fmt.Errorf("assertion %s: %w", name, err)
		}
		limit = float64(d.Milliseconds())
	default:
		return Assertion{}, fmt.Errorf("unknown assertion %s", name)
	}
	return Assertion{Name: name, Op: "<", Limit: limit, Raw: expr}, nil
}

// Judge compares a finished outcome with reliability assertions.
func Judge(out Outcome, rules []Assertion) []AssertionResult {
	if len(rules) == 0 {
		return nil
	}
	results := make([]AssertionResult, 0, len(rules))
	for _, rule := range rules {
		results = append(results, judgeOne(out, rule))
	}
	return results
}

// SummaryFailed reports whether a stored report JSON contains a failed assertion.
func SummaryFailed(summary string) bool {
	var body struct {
		Assertions []AssertionResult `json:"assertions"`
	}
	if err := json.Unmarshal([]byte(summary), &body); err != nil {
		return false
	}
	return Failed(body.Assertions)
}

// Failed reports whether any assertion failed.
func Failed(results []AssertionResult) bool {
	for _, result := range results {
		if !result.Pass {
			return true
		}
	}
	return false
}

func judgeOne(out Outcome, rule Assertion) AssertionResult {
	var actual float64
	var label string
	switch rule.Name {
	case "error_rate":
		actual = out.Failure.ErrorRate
		label = fmt.Sprintf("error rate %s (got %s)", rule.Raw, formatPercent(actual))
	case "p95_latency":
		actual = out.Failure.P95
		label = fmt.Sprintf("p95 latency %s (got %s)", rule.Raw, formatLatency(actual))
	case "recovery_time":
		if out.RecoveryMs == nil {
			return AssertionResult{Name: rule.Name, Pass: false, Detail: fmt.Sprintf("recovery time %s (not observed)", rule.Raw)}
		}
		actual = float64(*out.RecoveryMs)
		label = fmt.Sprintf("recovery time %s (got %.1fs)", rule.Raw, actual/1000)
	default:
		return AssertionResult{Name: rule.Name, Pass: false, Detail: "unknown assertion " + rule.Name}
	}
	return AssertionResult{Name: rule.Name, Pass: actual < rule.Limit, Detail: label}
}
