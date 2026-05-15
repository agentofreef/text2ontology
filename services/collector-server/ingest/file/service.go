package file

import (
	"bufio"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"os"

	"github.com/lakehouse2ontology/contracts"
)

// Service holds shared state for all file connector handlers.
type Service struct {
	DB         *sql.DB
	UploadRoot string // FILE_UPLOAD_ROOT env var (default /data/uploads)
	MaxBytes   int64  // COLLECTOR_FILE_MAX_BYTES env var (default 100 MB)
}

// writeError writes a contracts.ErrorEnvelope as JSON with the given status.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(contracts.ErrorEnvelope{
		Code:    code,
		Message: msg,
	})
}

// readCSVHeaders opens a CSV file and returns the first-row headers.
func readCSVHeaders(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReader(f))
	r.LazyQuotes = true
	r.TrimLeadingSpace = true

	row, err := r.Read()
	if err == io.EOF {
		return nil, nil
	}
	return row, err
}
