package llm

import (
	"testing"

	"github.com/thirdeye/overseer/internal/entity"
)

func auditHistory() []entity.EmailMessage {
	return []entity.EmailMessage{
		{ID: "m-1", WebLink: "https://outlook.example/m-1"},
		{ID: "m-2", WebLink: "https://outlook.example/m-2"},
	}
}

func validFinding() finding {
	return finding{
		Explanation:      "history agrees $41.00/unit; new message states $42.50/unit",
		Title:            "Price raised $41.00 → $42.50/unit",
		Priority:         "high",
		Type:             "price",
		PreviouslyAgreed: "$41.00/unit",
		NewMetric:        "$42.50/unit",
		Quotes:           []findingQuote{{Quote: "we agree on $41.00/unit", MessageIndex: 1}},
	}
}

func TestToFindingsMapsQuoteLinks(t *testing.T) {
	got, err := toFindings(findingsResult{Findings: []finding{validFinding()}}, auditHistory())
	if err != nil {
		t.Fatalf("toFindings: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	f := got[0]
	if f.Title != "Price raised $41.00 → $42.50/unit" || f.Priority != entity.PriorityHigh {
		t.Errorf("finding = %+v", f)
	}
	if f.Detail.Type != entity.MetricPrice || f.Detail.PreviouslyAgreed != "$41.00/unit" || f.Detail.NewMetric != "$42.50/unit" {
		t.Errorf("detail = %+v", f.Detail)
	}
	if len(f.Quotes) != 1 || f.Quotes[0].MessageLink != "https://outlook.example/m-2" {
		t.Errorf("quotes = %+v", f.Quotes)
	}
}

func TestToFindingsEmptyIsClean(t *testing.T) {
	got, err := toFindings(findingsResult{}, auditHistory())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want empty, nil", got, err)
	}
}

func rankCandidates() []entity.DriveItem {
	return []entity.DriveItem{
		{ID: "item-0", Name: "Orders 2026.xlsx", WebURL: "https://onedrive.example/orders"},
		{ID: "item-1", Name: "PO_Log.xlsx", WebURL: "https://onedrive.example/po-log"},
		{ID: "item-2", Name: "Holiday photos", WebURL: "https://onedrive.example/photos"},
		{ID: "item-3", Name: "Delmar quotes.xlsx", WebURL: "https://onedrive.example/delmar"},
	}
}

func TestToRankedResolvesIndexes(t *testing.T) {
	out := rankResult{Rankings: []ranking{
		{Explanation: "vendor-named order log", CandidateIndex: 3},
		{Explanation: "generic PO log", CandidateIndex: 1},
	}}
	got, err := toRanked(out, rankCandidates())
	if err != nil {
		t.Fatalf("toRanked: %v", err)
	}
	if len(got) != 2 || got[0].ID != "item-3" || got[1].ID != "item-1" {
		t.Errorf("ranked = %+v", got)
	}
}

func TestToRankedDedupesAndCapsAtThree(t *testing.T) {
	out := rankResult{Rankings: []ranking{
		{CandidateIndex: 0}, {CandidateIndex: 0}, // duplicate collapses
		{CandidateIndex: 1}, {CandidateIndex: 2}, {CandidateIndex: 3},
	}}
	got, err := toRanked(out, rankCandidates())
	if err != nil {
		t.Fatalf("toRanked: %v", err)
	}
	if len(got) != 3 || got[0].ID != "item-0" || got[1].ID != "item-1" || got[2].ID != "item-2" {
		t.Errorf("ranked = %+v", got)
	}
}

func TestToRankedRejectsBadIndex(t *testing.T) {
	for _, idx := range []int{-1, 4} {
		out := rankResult{Rankings: []ranking{{CandidateIndex: idx}}}
		if _, err := toRanked(out, rankCandidates()); err == nil {
			t.Errorf("index %d: want validation error, got nil", idx)
		}
	}
}

func TestToRankedEmptyIsValid(t *testing.T) {
	got, err := toRanked(rankResult{}, rankCandidates())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want empty, nil", got, err)
	}
}

func planStructure() entity.WorkbookStructure {
	return entity.WorkbookStructure{Tables: []entity.WorkbookTable{
		{Name: "PO_Log", Columns: []string{"PO", "Supplier", "ETA"}},
		{Name: "Contacts", Columns: []string{"Name", "Email"}},
	}}
}

func TestToPlanAcceptsMatchingTable(t *testing.T) {
	out := planResult{
		Explanation: "PO_Log is the order table",
		Table:       "PO_Log",
		Values:      []string{"4471", "Delmar Tooling", "2026-07-18"},
	}
	got, err := toPlan(out, planStructure())
	if err != nil {
		t.Fatalf("toPlan: %v", err)
	}
	if got.Table != "PO_Log" || len(got.Values) != 3 || got.Values[1] != "Delmar Tooling" {
		t.Errorf("plan = %+v", got)
	}
}

func TestToPlanRejectsBadOutput(t *testing.T) {
	cases := map[string]planResult{
		"unknown table":   {Table: "Sheet9", Values: []string{"a", "b", "c"}},
		"too few values":  {Table: "PO_Log", Values: []string{"4471"}},
		"too many values": {Table: "PO_Log", Values: []string{"a", "b", "c", "d"}},
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := toPlan(out, planStructure()); err == nil {
				t.Fatal("want validation error, got nil")
			}
		})
	}
}

func TestToFindingsRejectsBadOutput(t *testing.T) {
	cases := map[string]func(f *finding){
		"empty title":        func(f *finding) { f.Title = "" },
		"invalid priority":   func(f *finding) { f.Priority = "urgent" },
		"invalid type":       func(f *finding) { f.Type = "weight" },
		"index out of range": func(f *finding) { f.Quotes[0].MessageIndex = 2 },
		"negative index":     func(f *finding) { f.Quotes[0].MessageIndex = -1 },
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			f := validFinding()
			corrupt(&f)
			if _, err := toFindings(findingsResult{Findings: []finding{f}}, auditHistory()); err == nil {
				t.Fatal("want validation error, got nil")
			}
		})
	}
}
