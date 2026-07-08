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
