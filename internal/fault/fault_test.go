package fault

import (
	"errors"
	"strings"
	"testing"
)

func TestPrivilegedRetryRequiresFlag(t *testing.T) {
	if allowPrivilegedRetry(false, "operation not permitted") {
		t.Fatal("privileged retry ran without the flag")
	}
	if !allowPrivilegedRetry(true, "net_admin") {
		t.Fatal("privileged retry did not run when requested")
	}
	if allowPrivilegedRetry(true, "no such container") {
		t.Fatal("unrelated error retried privileged")
	}
}

func TestClearContinuesAfterNetemFailure(t *testing.T) {
	var calls []string
	err := performClear(clearActions{
		running: func() (bool, error) { return true, nil },
		netem: func() error {
			calls = append(calls, "netem")
			return errors.New("qdisc")
		},
		fallback: func() error { calls = append(calls, "fallback"); return nil },
		cpu:      func() error { calls = append(calls, "cpu"); return nil },
		start:    func() error { calls = append(calls, "start"); return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "qdisc") {
		t.Fatalf("err = %v", err)
	}
	if strings.Join(calls, ",") != "netem,fallback,cpu,start" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestClearSkipsNetemWhenStoppedButStarts(t *testing.T) {
	var calls []string
	err := performClear(clearActions{
		running:  func() (bool, error) { return false, nil },
		netem:    func() error { calls = append(calls, "netem"); return nil },
		fallback: func() error { calls = append(calls, "fallback"); return nil },
		cpu:      func() error { calls = append(calls, "cpu"); return nil },
		start:    func() error { calls = append(calls, "start"); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "fallback,cpu,start" {
		t.Fatalf("calls = %v", calls)
	}
}
