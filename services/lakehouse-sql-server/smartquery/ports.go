// Package smartquery owns the LLM-facing QuerySpec family of types and a few
// pure-string helpers (NormalizeQuerySpec, StripObjectPrefix,
// StripDateGranularity, StripAggWrapper) used by the lakehouse SQL pipeline.
//
// Historically this package also hosted a DAX generator (BuildDAX,
// GenerateDAX, DAXExecutor, Engine interface). The lakehouse-only branch
// abandoned DAX entirely; that surface has been removed. Real query
// execution lives in `lakehouse.Engine` (ontology/lakehouse/engine.go),
// which depends on the types declared here but generates PostgreSQL — not DAX.
package smartquery
