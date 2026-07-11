package otto

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
)

type resolveFixture struct {
	uc    *ResolveDestinations
	in    ResolveInput
	graph *testutil.FakeGraph
	drive *testutil.FakeDrive
	llm   *testutil.FakeLLM
	repo  *testutil.InMemOttoScans
}

func newResolveFixture() *resolveFixture {
	graph := &testutil.FakeGraph{ConversationsByID: map[string][]entity.EmailMessage{}}
	drive := &testutil.FakeDrive{SearchResults: map[string][]entity.DriveItem{}}
	llm := &testutil.FakeLLM{Extractions: map[string]entity.OrderExtraction{}}
	repo := testutil.NewInMemOttoScans()
	return &resolveFixture{
		uc: &ResolveDestinations{Graph: graph, Drive: drive, LLM: llm, Repo: repo},
		in: ResolveInput{
			AccountID: uuid.New(), UserID: uuid.New(),
			GraphToken: "tok", MessageID: "msg-2", ConversationID: "conv-1",
		},
		graph: graph, drive: drive, llm: llm, repo: repo,
	}
}

func seedThread(f *resolveFixture) {
	f.graph.ConversationsByID["conv-1"] = []entity.EmailMessage{
		{ID: "msg-1", ConversationID: "conv-1", CreatedDateTime: time.Now().Add(-time.Hour)},
		{ID: "msg-2", ConversationID: "conv-1", WebLink: "https://outlook.example/msg-2", CreatedDateTime: time.Now()},
	}
	f.llm.Extractions["conv-1"] = entity.OrderExtraction{
		CompanyName: "Delmar Tooling", OrderID: 4471, Status: entity.OrderStatusConfirmed,
	}
}

func item(id, name string) entity.DriveItem {
	return entity.DriveItem{ID: id, Name: name, WebURL: "https://onedrive.example/" + id}
}

func TestResolveHappyPath(t *testing.T) {
	f := newResolveFixture()
	seedThread(f)
	f.drive.SearchResults["Delmar Tooling"] = []entity.DriveItem{item("item-1", "Delmar orders.xlsx")}
	f.drive.SearchResults["4471"] = []entity.DriveItem{item("item-2", "PO_Log.xlsx")}
	f.drive.RecentItems = []entity.DriveItem{item("item-3", "Recent.xlsx")}
	f.llm.RankResult = []entity.DriveItem{item("item-1", "Delmar orders.xlsx"), item("item-2", "PO_Log.xlsx")}

	out, err := f.uc.Execute(context.Background(), f.in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// loop order: extract → search → rank
	if f.llm.ExtractCalls != 1 || f.llm.RankCalls != 1 {
		t.Errorf("extract=%d rank=%d, want 1, 1", f.llm.ExtractCalls, f.llm.RankCalls)
	}
	// the four spec queries: supplier, PO number, "orders", "PO" (+ recent)
	for _, q := range []string{"Delmar Tooling", "4471", "orders", "PO"} {
		if !slices.Contains(f.drive.QueriesSeen, q) {
			t.Errorf("missing search query %q in %v", q, f.drive.QueriesSeen)
		}
	}
	if f.drive.RecentCalls != 1 {
		t.Errorf("recent calls = %d, want 1", f.drive.RecentCalls)
	}
	if len(out.Destinations) != 2 || out.Destinations[0].ID != "item-1" {
		t.Errorf("destinations = %+v", out.Destinations)
	}

	// scan stored before returning, stamped with ownership + extraction
	if f.repo.Count() != 1 {
		t.Fatalf("scan rows = %d, want 1", f.repo.Count())
	}
	if out.Scan.ID == uuid.Nil {
		t.Error("scan has no server-minted id")
	}
	if out.Scan.AccountID != f.in.AccountID || out.Scan.UserID != f.in.UserID ||
		out.Scan.MessageID != "msg-2" || out.Scan.ConversationID != "conv-1" ||
		out.Scan.Fields.CompanyName != "Delmar Tooling" ||
		out.Scan.EmailURL != "https://outlook.example/msg-2" ||
		out.Scan.Status != entity.ScanScanned {
		t.Errorf("scan = %+v", out.Scan)
	}
}

func TestResolveDedupesCandidatesAcrossSearches(t *testing.T) {
	f := newResolveFixture()
	seedThread(f)
	// the same file comes back from two searches and from recent
	dup := item("item-1", "PO_Log.xlsx")
	f.drive.SearchResults["Delmar Tooling"] = []entity.DriveItem{dup}
	f.drive.SearchResults["PO"] = []entity.DriveItem{dup}
	f.drive.RecentItems = []entity.DriveItem{dup, item("item-2", "Other.xlsx")}

	if _, err := f.uc.Execute(context.Background(), f.in); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(f.llm.RankCandidatesSeen) != 1 {
		t.Fatalf("rank calls = %d, want 1", len(f.llm.RankCandidatesSeen))
	}
	got := f.llm.RankCandidatesSeen[0]
	if len(got) != 2 || got[0] != "item-1" || got[1] != "item-2" {
		t.Errorf("candidates = %v, want deduped [item-1 item-2]", got)
	}
}

func TestResolveSkipsPOQueryWhenNoOrderID(t *testing.T) {
	f := newResolveFixture()
	seedThread(f)
	f.llm.Extractions["conv-1"] = entity.OrderExtraction{CompanyName: "Delmar Tooling"} // OrderID 0

	if _, err := f.uc.Execute(context.Background(), f.in); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if slices.Contains(f.drive.QueriesSeen, "0") {
		t.Errorf("searched for the zero PO number: %v", f.drive.QueriesSeen)
	}
}

func TestResolveNoCandidatesSkipsRanking(t *testing.T) {
	f := newResolveFixture()
	seedThread(f)
	// no search results, no recent items

	out, err := f.uc.Execute(context.Background(), f.in)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if f.llm.RankCalls != 0 {
		t.Error("rank ran with zero candidates")
	}
	if len(out.Destinations) != 0 || f.repo.Count() != 1 {
		t.Errorf("destinations=%d scans=%d, want 0 destinations, 1 scan", len(out.Destinations), f.repo.Count())
	}
}

func TestResolveEmptyConversationErrors(t *testing.T) {
	f := newResolveFixture()
	// no thread seeded
	if _, err := f.uc.Execute(context.Background(), f.in); err == nil {
		t.Fatal("want error for empty conversation")
	}
	if f.repo.Count() != 0 {
		t.Error("scan stored despite failure")
	}
}

func TestResolveExtractErrorStoresNoScan(t *testing.T) {
	f := newResolveFixture()
	seedThread(f)
	f.llm.ExtractErr = errors.New("model 503")

	if _, err := f.uc.Execute(context.Background(), f.in); err == nil {
		t.Fatal("want error from extraction failure")
	}
	if f.repo.Count() != 0 {
		t.Error("scan row leaked on failure")
	}
}
