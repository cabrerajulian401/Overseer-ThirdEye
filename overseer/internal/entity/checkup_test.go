package entity

import "testing"

func TestValidCheckupStatus(t *testing.T) {
	for _, s := range []CheckupStatus{CheckupPending, CheckupPaused, CheckupSending, CheckupSent, CheckupCancelled} {
		if !ValidCheckupStatus(s) {
			t.Errorf("ValidCheckupStatus(%q) = false, want true", s)
		}
	}
	if ValidCheckupStatus("snoozed") {
		t.Error(`ValidCheckupStatus("snoozed") = true, want false`)
	}
}

func TestCanTransitionCheckup(t *testing.T) {
	cases := []struct {
		from, to CheckupStatus
		want     bool
	}{
		// paused ↔ pending, both may cancel; same-status PATCH is an allowed no-op
		{CheckupPending, CheckupPaused, true},
		{CheckupPending, CheckupCancelled, true},
		{CheckupPending, CheckupPending, true},
		{CheckupPaused, CheckupPending, true},
		{CheckupPaused, CheckupCancelled, true},
		{CheckupPaused, CheckupPaused, true},
		// sent/cancelled are terminal
		{CheckupSent, CheckupPaused, false},
		{CheckupSent, CheckupPending, false},
		{CheckupCancelled, CheckupPending, false},
		{CheckupCancelled, CheckupCancelled, false},
		// sending belongs to the scheduler
		{CheckupSending, CheckupCancelled, false},
		{CheckupSending, CheckupPaused, false},
		// clients can never move a row INTO sent or sending
		{CheckupPending, CheckupSent, false},
		{CheckupPending, CheckupSending, false},
	}
	for _, c := range cases {
		if got := CanTransitionCheckup(c.from, c.to); got != c.want {
			t.Errorf("CanTransitionCheckup(%q, %q) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}
