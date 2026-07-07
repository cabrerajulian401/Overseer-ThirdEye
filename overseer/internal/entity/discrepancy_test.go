package entity

import "testing"

func TestValidDiscrepancyStatus(t *testing.T) {
	for _, s := range []DiscrepancyStatus{DiscrepancyOpen, DiscrepancyResolved, DiscrepancyDismissed} {
		if !ValidDiscrepancyStatus(s) {
			t.Errorf("ValidDiscrepancyStatus(%q) = false, want true", s)
		}
	}
	if ValidDiscrepancyStatus("archived") {
		t.Error(`ValidDiscrepancyStatus("archived") = true, want false`)
	}
}

func TestValidPriority(t *testing.T) {
	for _, p := range []Priority{PriorityHigh, PriorityMedium, PriorityLow} {
		if !ValidPriority(p) {
			t.Errorf("ValidPriority(%q) = false, want true", p)
		}
	}
	if ValidPriority("urgent") {
		t.Error(`ValidPriority("urgent") = true, want false`)
	}
}

func TestValidMetricType(t *testing.T) {
	for _, m := range []MetricType{MetricPrice, MetricDate, MetricUnit} {
		if !ValidMetricType(m) {
			t.Errorf("ValidMetricType(%q) = false, want true", m)
		}
	}
	if ValidMetricType("weight") {
		t.Error(`ValidMetricType("weight") = true, want false`)
	}
}
