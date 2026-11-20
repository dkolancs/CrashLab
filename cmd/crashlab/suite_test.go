package main

import "testing"

func TestSuiteStopsOnlyOnCleanupFailure(t *testing.T) {
	if !suiteShouldStop("cleanup failed: qdisc delete") {
		t.Fatal("cleanup failure should stop the suite")
	}
	if suiteShouldStop("experiment failed assertions") || suiteShouldStop("experiment passed assertions; expected failure") {
		t.Fatal("assertion result should not stop the suite")
	}
}
