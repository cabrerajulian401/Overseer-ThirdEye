package otto

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/thirdeye/overseer/internal/entity"
	"github.com/thirdeye/overseer/internal/testutil"
)

type recordFixture struct {
	uc    *RecordOrder
	scan  entity.OttoScan
	repo  *testutil.InMemOttoScans
	excel *testutil.FakeExcel
	llm   *testutil.FakeLLM
	drive *testutil.FakeDrive
}

func newRecordFixture(t *testing.T) *recordFixture {
	t.Helper()
	repo := testutil.NewInMemOttoScans()
	excel := &testutil.FakeExcel{Structures: map[string]entity.WorkbookStructure{
		"item-1": {Tables: []entity.WorkbookTable{{Name: "PO_Log", Columns: []string{"PO", "Supplier", "ETA"}}}},
	}}
	llm := &testutil.FakeLLM{PlanResult: entity.SheetAppendPlan{
		Table: "PO_Log", Values: []string{"4471", "Delmar Tooling", "2026-07-18"},
	}}
	drive := &testutil.FakeDrive{Items: map[string]entity.DriveItem{
		"item-1": {ID: "item-1", Name: "Orders 2026.xlsx", WebURL: "https://onedrive.example/orders-2026"},
	}}

	scan, err := repo.Insert(context.Background(), entity.OttoScan{
		AccountID: uuid.New(), UserID: uuid.New(),
		MessageID: "msg-2", ConversationID: "conv-1",
		Fields: entity.OrderExtraction{CompanyName: "Delmar Tooling", OrderID: 4471}, // the RAW extraction
		Status: entity.ScanScanned,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &recordFixture{
		uc:   &RecordOrder{Repo: repo, Excel: excel, LLM: llm, Drive: drive},
		scan: scan, repo: repo, excel: excel, llm: llm, drive: drive,
	}
}

func (f *recordFixture) input() RecordInput {
	return RecordInput{
		UserID: f.scan.UserID, GraphToken: "tok",
		ScanID: f.scan.ID, SelectedResource: "item-1",
		// the user EDITED the company name on the confirm card
		Fields: entity.OrderExtraction{CompanyName: "Delmar Tooling Inc.", OrderID: 4471},
	}
}

func TestRecordWritesApprovedFieldsNotStoredOnes(t *testing.T) {
	f := newRecordFixture(t)

	webURL, err := f.uc.Execute(context.Background(), f.input())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if webURL != "https://onedrive.example/orders-2026" {
		t.Errorf("webURL = %q", webURL)
	}

	// the plan saw the user-approved fields — not the stored raw extraction
	if len(f.llm.PlanFieldsSeen) != 1 || f.llm.PlanFieldsSeen[0].CompanyName != "Delmar Tooling Inc." {
		t.Errorf("plan fields = %+v, want the edited company name", f.llm.PlanFieldsSeen)
	}
	// the gateway wrote exactly the plan
	if len(f.excel.Appends) != 1 {
		t.Fatalf("appends = %d, want 1", len(f.excel.Appends))
	}
	a := f.excel.Appends[0]
	if a.ItemID != "item-1" || a.Table != "PO_Log" || a.Values[1] != "Delmar Tooling" {
		t.Errorf("append = %+v", a)
	}
	// scan marked recorded with the chosen resource
	saved, _, _ := f.repo.GetByID(context.Background(), f.scan.UserID, f.scan.ID)
	if saved.Status != entity.ScanRecorded || saved.SelectedResource != "item-1" {
		t.Errorf("scan after record = %+v", saved)
	}
}

func TestRecordUnknownScanNotFound(t *testing.T) {
	f := newRecordFixture(t)
	in := f.input()
	in.ScanID = uuid.New()

	if _, err := f.uc.Execute(context.Background(), in); !errors.Is(err, ErrScanNotFound) {
		t.Fatalf("err = %v, want ErrScanNotFound", err)
	}
	if len(f.excel.Appends) != 0 {
		t.Error("append happened for unknown scan")
	}
}

func TestRecordOtherUsersScanNotFound(t *testing.T) {
	f := newRecordFixture(t)
	in := f.input()
	in.UserID = uuid.New()

	if _, err := f.uc.Execute(context.Background(), in); !errors.Is(err, ErrScanNotFound) {
		t.Fatalf("err = %v, want ErrScanNotFound", err)
	}
}

func TestRecordTwiceConflicts(t *testing.T) {
	f := newRecordFixture(t)
	if _, err := f.uc.Execute(context.Background(), f.input()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.uc.Execute(context.Background(), f.input()); !errors.Is(err, ErrScanAlreadyRecorded) {
		t.Fatalf("second record err = %v, want ErrScanAlreadyRecorded", err)
	}
	if len(f.excel.Appends) != 1 {
		t.Errorf("appends = %d, want 1 (no double write)", len(f.excel.Appends))
	}
}

func TestRecordAppendFailureLeavesScanScanned(t *testing.T) {
	f := newRecordFixture(t)
	f.excel.AppendErr = errors.New("graph 503")

	if _, err := f.uc.Execute(context.Background(), f.input()); err == nil {
		t.Fatal("want error from append failure")
	}
	saved, _, _ := f.repo.GetByID(context.Background(), f.scan.UserID, f.scan.ID)
	if saved.Status != entity.ScanScanned {
		t.Errorf("status = %q, want scanned (not recorded on failure)", saved.Status)
	}
}
