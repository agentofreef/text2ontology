// Package pbitlakehouse: parallel PBIT→pg lakehouse import path. Must NOT import smartquery or the parent ingest package.
package pbit

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// FlexString handles PBIT JSON fields that can be either a plain string or
// an array of strings (multi-line M expressions or measure expressions).
type FlexString string

func (f *FlexString) UnmarshalJSON(data []byte) error {
	// Try string first
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*f = FlexString(s)
		return nil
	}
	// Try []string — join with newline
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*f = FlexString(strings.Join(arr, "\n"))
		return nil
	}
	// Fallback: store raw
	*f = FlexString(string(data))
	return nil
}

func (f FlexString) String() string { return string(f) }

// PbitSchema is the top-level parsed representation of a PBIT DataModelSchema.
type PbitSchema struct {
	Tables        []PbitTable        `json:"tables"`
	Relationships []PbitRelationship `json:"relationships"`
}

// PbitTable represents a Power BI table (entity / dimension / fact).
type PbitTable struct {
	Name       string          `json:"name"`
	IsHidden   bool            `json:"isHidden"`
	Columns    []PbitColumn    `json:"columns"`
	Partitions []PbitPartition `json:"partitions"`
	Measures   []PbitMeasure   `json:"measures"`
}

// PbitColumn is a column definition inside a PBIT table.
type PbitColumn struct {
	Name        string     `json:"name"`
	DataType    string     `json:"dataType"`
	IsHidden    bool       `json:"isHidden"`
	Description string     `json:"description"`
	Expression  FlexString `json:"expression"` // only set for calculated columns
}

// PbitPartition is a data partition inside a PBIT table.
// Source contains the raw M expression string for further classification.
type PbitPartition struct {
	Name   string      `json:"name"`
	Source PbitPartSrc `json:"source"`
}

// PbitPartSrc is the source definition block inside a partition.
type PbitPartSrc struct {
	Type       string     `json:"type"`
	Expression FlexString `json:"expression"` // raw M expression (retained verbatim)
}

// PbitRelationship is a column-level join between two tables.
type PbitRelationship struct {
	Name                      string `json:"name"`
	FromTable                 string `json:"fromTable"`
	FromColumn                string `json:"fromColumn"`
	ToTable                   string `json:"toTable"`
	ToColumn                  string `json:"toColumn"`
	CrossFilteringBehavior    string `json:"crossFilteringBehavior"`
	FromCardinality           string `json:"fromCardinality"`
	ToCardinality             string `json:"toCardinality"`
	IsActive                  bool   `json:"isActive"`
	SecurityFilteringBehavior string `json:"securityFilteringBehavior"`
}

// PbitMeasure is measure metadata from a PBIT table; DAX expression from PBIT format is parsed but discarded (system uses SQL only).
type PbitMeasure struct {
	Name         string     `json:"name"`
	Expression   FlexString `json:"expression"` // PBIT measure expression — parsed but discarded; the system generates SQL, not DAX
	FormatString string     `json:"formatString"`
	Description  string     `json:"description"`
	// TableName is set by ParsePbit after the measure is lifted from its parent table.
	TableName string `json:"-"`
}

// pbitDataModelSchema is the raw JSON wrapper produced by Power BI Desktop.
// We only care about the nested "model" object.
type pbitDataModelSchema struct {
	Model struct {
		Tables        []PbitTable        `json:"tables"`
		Relationships []PbitRelationship `json:"relationships"`
	} `json:"model"`
}

// ParsePbit opens the .pbit file (a ZIP archive), locates the DataModelSchema
// entry, decodes UTF-16 LE (stripping BOM if present), and unmarshals the JSON
// into a PbitSchema.  Measures are lifted to the top level and annotated with
// their parent table name.
func ParsePbit(path string) (*PbitSchema, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("pbitlakehouse: open zip %q: %w", path, err)
	}
	defer zr.Close()

	// Locate the DataModelSchema entry (case-insensitive).
	var entry *zip.File
	for _, f := range zr.File {
		if strings.EqualFold(f.Name, "DataModelSchema") {
			entry = f
			break
		}
	}
	if entry == nil {
		return nil, fmt.Errorf("pbitlakehouse: DataModelSchema not found in %q", path)
	}

	rc, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("pbitlakehouse: open DataModelSchema: %w", err)
	}
	defer rc.Close()

	// Read all bytes first so we can strip the BOM before the decoder sees it.
	raw, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("pbitlakehouse: read DataModelSchema: %w", err)
	}

	// Strip UTF-16 LE BOM (0xFF 0xFE) if present.
	payload := raw
	if len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE {
		payload = raw[2:]
	}

	// Decode UTF-16 LE → UTF-8.
	dec := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder()
	utf8Bytes, _, err := transform.Bytes(dec, payload)
	if err != nil {
		return nil, fmt.Errorf("pbitlakehouse: decode UTF-16 LE: %w", err)
	}

	var wrapper pbitDataModelSchema
	if err := json.Unmarshal(utf8Bytes, &wrapper); err != nil {
		return nil, fmt.Errorf("pbitlakehouse: unmarshal DataModelSchema: %w", err)
	}

	schema := &PbitSchema{
		Tables:        wrapper.Model.Tables,
		Relationships: wrapper.Model.Relationships,
	}

	// Lift measures to top-level annotations (TableName set here).
	for i := range schema.Tables {
		for j := range schema.Tables[i].Measures {
			schema.Tables[i].Measures[j].TableName = schema.Tables[i].Name
		}
	}

	return schema, nil
}

// AllMeasures returns all measures across all tables in a flat slice.
func (s *PbitSchema) AllMeasures() []PbitMeasure {
	var out []PbitMeasure
	for _, t := range s.Tables {
		out = append(out, t.Measures...)
	}
	return out
}
