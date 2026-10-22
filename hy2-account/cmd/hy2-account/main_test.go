package main

import "testing"

func TestQuotaBytesUsesExactDecimalGiBAndRejectsAmbiguity(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "141", "NaN", "Inf", "1e2", "1/2", "1.2.3", " 1", "0.000000000000001"} {
		if _, err := quotaBytes(value); err == nil {
			t.Fatalf("invalid decimal quota accepted: %q", value)
		}
	}
	for value, expected := range map[string]int64{"1": 1073741824, "1.25": 1342177280, "0.1": 107374182, "140": 150323855360} {
		got, err := quotaBytes(value)
		if err != nil || got != expected {
			t.Fatalf("%s: got %d, %v; want %d", value, got, err, expected)
		}
	}
}
