package entity

// DriveItem is a OneDrive file — a candidate or chosen ERP destination.
type DriveItem struct {
	ID     string
	Name   string
	WebURL string
}

// WorkbookTable is one table in an Excel workbook: its name and column
// headers, in sheet order.
type WorkbookTable struct {
	Name    string
	Columns []string
}

// WorkbookStructure is what gateway/excel reads so the LLM can plan an
// append without ever seeing the workbook's data rows.
type WorkbookStructure struct {
	Tables []WorkbookTable
}

// SheetAppendPlan is gateway/llm.PlanSheetAppend's output: which table to
// append to and one value per column, in column order. The LLM decides
// where/how — never the values themselves.
type SheetAppendPlan struct {
	Table  string
	Values []string
}
