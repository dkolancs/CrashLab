package analysis

import (
	"strings"
	"testing"
)

func TestHistorySeparatesKnownBadFromPassRate(t *testing.T) {
	first := int64(200)
	second := int64(2400)
	known := int64(100)
	runs := []SavedRun{
		saved(1, "ci-smoke", false, 10, "System tolerated the fault.", true, &first),
		saved(2, "ci-smoke", false, 10, "System degraded significantly.", false, &second),
		saved(3, "payment-latency", true, 10, "System degraded significantly.", false, &known),
	}
	items := History(runs)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	smoke := items[0]
	if smoke.Name != "ci-smoke" || smoke.PassText != "1/2" || smoke.Judged != 2 || smoke.Passed != 1 {
		t.Fatalf("smoke = %#v", smoke)
	}
	if len(smoke.Recovery) != 2 || smoke.Recovery[0].RecoveryMs == nil || *smoke.Recovery[0].RecoveryMs != 200 || *smoke.Recovery[1].RecoveryMs != 2400 {
		t.Fatalf("recovery = %#v", smoke.Recovery)
	}
	if !strings.Contains(smoke.Trend, "2.2s slower") {
		t.Fatalf("trend = %s", smoke.Trend)
	}
	bad := items[1]
	if bad.Name != "payment-latency" || bad.Judged != 0 || len(bad.KnownBad) != 1 {
		t.Fatalf("known bad = %#v", bad)
	}
	text := FormatHistory(items)
	for _, part := range []string{
		"ci-smoke",
		"pass rate 1/2",
		"recovery #001 0.2s",
		"recovery #002 2.4s",
		"payment-latency",
		"known bad, not in the pass rate",
		"error rate 80%",
	} {
		if !strings.Contains(text, part) {
			t.Fatalf("history missing %q\n%s", part, text)
		}
	}
	if strings.Count(text, "pass rate 1/2") != 1 || strings.Contains(text, "pass rate 0/") || strings.Contains(text, "pass rate 1/3") {
		t.Fatalf("known-bad file changed the pass rate\n%s", text)
	}
}

func TestHistoryTrendNeedsTwoJudgedRuns(t *testing.T) {
	ms := int64(200)
	items := History([]SavedRun{saved(1, "ci-smoke", false, 10, "System tolerated the fault.", true, &ms)})
	if items[0].Trend != "trend is not established" || items[0].PassText != "1/1" {
		t.Fatalf("item = %#v", items[0])
	}
	short := saved(2, "ci-smoke", false, 3, "Not enough data", true, &ms)
	items = History([]SavedRun{short})
	if items[0].Judged != 0 || items[0].PassText != "not established" {
		t.Fatalf("short run was judged: %#v", items[0])
	}
}

func saved(id int64, name string, expect bool, count int, conclusion string, pass bool, recovery *int64) SavedRun {
	return SavedRun{
		ID: id, Name: name, ExpectFailure: expect,
		Outcome: Outcome{
			Baseline:   Window{Count: count, SuccessCount: count},
			Failure:    Window{Count: count, SuccessCount: count, ErrorRate: 0.8},
			Conclusion: conclusion,
			RecoveryMs: recovery,
			Assertions: []AssertionResult{{Name: "error_rate", Pass: pass, Detail: "error rate 80%"}},
		},
	}
}
