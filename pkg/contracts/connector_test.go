package contracts_test

import (
	"encoding/json"
	"testing"

	"contracts"
)

func TestDataSourceCreateReqRoundtrip(t *testing.T) {
	orig := DataSourceCreateReq{
		ProjectID:  "proj-1",
		Type:       "pbi",
		Label:      "My PBI source",
		ConfigJSON: map[string]any{"workspace": "default"},
	}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got DataSourceCreateReq
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ProjectID != orig.ProjectID || got.Type != orig.Type || got.Label != orig.Label {
		t.Errorf("roundtrip mismatch: got %+v", got)
	}
}

func TestCatalogRespRoundtrip(t *testing.T) {
	rc := int64(100)
	orig := CatalogResp{
		Tables: []TableInfo{
			{
				Name:     "Orders",
				Columns:  []ColumnInfo{{Name: "id", DataType: "int4"}, {Name: "qty", DataType: "float8"}},
				ForeignKeys: []FKInfo{{FromTable: "Orders", FromColumn: "customer_id", ToTable: "Customers", ToColumn: "id"}},
				RowCount: &rc,
			},
		},
	}
	b, _ := json.Marshal(orig)
	var got CatalogResp
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Tables) != 1 || got.Tables[0].Name != "Orders" {
		t.Errorf("roundtrip mismatch: got %+v", got)
	}
	if got.Tables[0].RowCount == nil || *got.Tables[0].RowCount != 100 {
		t.Errorf("RowCount roundtrip failed")
	}
}

func TestWizardStateUpdateRoundtrip(t *testing.T) {
	orig := WizardStateUpdate{
		TableRoles:  map[string]string{"Orders": "fact", "Customers": "dim"},
		ColumnRoles: map[string]map[string]string{"Orders": {"id": "key", "qty": "measure"}},
		LinkDecisions: []LinkDecision{
			{FromTable: "Orders", FromColumn: "customer_id", ToTable: "Customers", ToColumn: "id", Create: true},
		},
	}
	b, _ := json.Marshal(orig)
	var got WizardStateUpdate
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.TableRoles["Orders"] != "fact" || !got.LinkDecisions[0].Create {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestSyncProgressEventRoundtrip(t *testing.T) {
	orig := SyncProgressEvent{Phase: "sync_complete", TableName: "Orders", RowsSynced: 42}
	b, _ := json.Marshal(orig)
	var got SyncProgressEvent
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Phase != "sync_complete" || got.RowsSynced != 42 {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

func TestTestConnectionReqResp(t *testing.T) {
	req := TestConnectionReq{Host: "localhost", Port: 5432, Database: "mydb", User: "admin", Password: "s3cr3t"}
	b, _ := json.Marshal(req)
	var got TestConnectionReq
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal req: %v", err)
	}
	if got.Port != 5432 || got.Database != "mydb" {
		t.Errorf("req roundtrip mismatch: %+v", got)
	}

	resp := TestConnectionResp{OK: true, Message: "connected"}
	rb, _ := json.Marshal(resp)
	var gotResp TestConnectionResp
	if err := json.Unmarshal(rb, &gotResp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if !gotResp.OK {
		t.Errorf("resp roundtrip mismatch: %+v", gotResp)
	}
}

func TestErrorEnvelopeRoundtrip(t *testing.T) {
	orig := ErrorEnvelope{Code: "SSRF_BLOCKED", Message: "target host is private", Details: map[string]any{"host": "10.0.0.1"}}
	b, _ := json.Marshal(orig)
	var got ErrorEnvelope
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Code != "SSRF_BLOCKED" || got.Message != "target host is private" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
}

// Alias the contracts types into the test package scope for cleaner syntax.
type DataSourceCreateReq = contracts.DataSourceCreateReq
type CatalogResp = contracts.CatalogResp
type TableInfo = contracts.TableInfo
type ColumnInfo = contracts.ColumnInfo
type FKInfo = contracts.FKInfo
type WizardStateUpdate = contracts.WizardStateUpdate
type LinkDecision = contracts.LinkDecision
type SyncProgressEvent = contracts.SyncProgressEvent
type TestConnectionReq = contracts.TestConnectionReq
type TestConnectionResp = contracts.TestConnectionResp
type ErrorEnvelope = contracts.ErrorEnvelope
