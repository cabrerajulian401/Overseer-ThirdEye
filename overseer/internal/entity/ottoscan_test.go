package entity

import "testing"

func TestValidScanStatus(t *testing.T) {
	for _, s := range []ScanStatus{ScanScanned, ScanRecorded, ScanAbandoned} {
		if !ValidScanStatus(s) {
			t.Errorf("ValidScanStatus(%q) = false, want true", s)
		}
	}
	if ValidScanStatus("pending") {
		t.Error(`ValidScanStatus("pending") = true, want false`)
	}
}
