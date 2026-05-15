package contracts

// DataSourceCreateReq — 创建数据源请求
type DataSourceCreateReq struct {
	ProjectID  string         `json:"project_id"`
	Type       string         `json:"type"`        // postgres / file / pbi
	Label      string         `json:"label"`
	ConfigJSON map[string]any `json:"config_json"` // type-specific config
}

// DataSourceCreateResp — 创建数据源响应
type DataSourceCreateResp struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// CatalogResp — catalog discovery 响应
type CatalogResp struct {
	Tables []TableInfo `json:"tables"`
}

// TableInfo describes a single table in a discovered catalog.
type TableInfo struct {
	Name        string       `json:"name"`
	Columns     []ColumnInfo `json:"columns"`
	ForeignKeys []FKInfo     `json:"foreign_keys"`
	RowCount    *int64       `json:"row_count,omitempty"`
}

// ColumnInfo describes a single column within a TableInfo.
type ColumnInfo struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
}

// FKInfo describes a foreign-key relationship between two tables.
type FKInfo struct {
	FromTable  string `json:"from_table"`
	FromColumn string `json:"from_column"`
	ToTable    string `json:"to_table"`
	ToColumn   string `json:"to_column"`
}

// WizardStateUpdate — 向导状态更新（表/列角色 + 关系决策）
type WizardStateUpdate struct {
	TableRoles    map[string]string            `json:"table_roles"`   // table_name → dim/fact/skip
	ColumnRoles   map[string]map[string]string `json:"column_roles"`  // table_name → column_name → key/measure/attribute/skip
	LinkDecisions []LinkDecision               `json:"link_decisions"`
}

// LinkDecision records whether to create an ont_link_type for a detected FK.
type LinkDecision struct {
	FromTable  string `json:"from_table"`
	FromColumn string `json:"from_column"`
	ToTable    string `json:"to_table"`
	ToColumn   string `json:"to_column"`
	Create     bool   `json:"create"` // true = create ont_link_type
}

// SyncProgressEvent — SSE 同步进度事件
type SyncProgressEvent struct {
	Phase       string `json:"phase"`                   // sync_started / sync_progress / sync_complete / sync_failed
	TableName   string `json:"table_name,omitempty"`
	RowsSynced  int64  `json:"rows_synced,omitempty"`
	BytesSynced int64  `json:"bytes_synced,omitempty"`
	Error       string `json:"error,omitempty"`
}

// TestConnectionReq — 连接测试请求
type TestConnectionReq struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Schema   string `json:"schema,omitempty"`
	User     string `json:"user"`
	Password string `json:"password"`
}

// TestConnectionResp — 连接测试响应
type TestConnectionResp struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// ErrorEnvelope — 统一错误响应
type ErrorEnvelope struct {
	Code    string `json:"code"`             // e.g. "SSRF_BLOCKED", "SYNC_FAILED"
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}
