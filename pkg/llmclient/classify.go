package llmclient

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/lakehouse2ontology/httputil"
)

// PbitColumnInfo holds column metadata for classification.
type PbitColumnInfo struct {
	ID           string
	TableName    string
	ColumnName   string
	DataType     string
	SampleValues []string
}

// ClassifyResult holds the result of a single column classification.
type ClassifyResult struct {
	MC     bool
	Reason string
}

// ClassifyMachineCode concurrently calls LLM once per column to classify as machine code.
// Updates is_machine_code in DB. Returns count of machine code columns.
// Optional onProgress callback is called every 20 columns with (done, total, mcSoFar).
func ClassifyMachineCode(db *sql.DB, columns []PbitColumnInfo, onProgress ...func(done, total, mc int)) int {
	if len(columns) == 0 {
		return 0
	}
	baseURL, apiKey, modelName, isThinking, vendor := GetActiveChatConfig(db)
	if baseURL == "" || modelName == "" {
		log.Printf("PBIT: no active chat LLM config, skipping machine code classification")
		return 0
	}

	const concurrency = 10
	sem := make(chan struct{}, concurrency)
	var mu sync.Mutex
	var machineCount int32
	var doneCount int32
	total := len(columns)
	client := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{Proxy: nil, MaxIdleConnsPerHost: concurrency, TLSClientConfig: TLSClientConfig()}}

	var wg sync.WaitGroup
	for _, col := range columns {
		wg.Add(1)
		go func(c PbitColumnInfo) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			cr := ClassifySingleColumn(c, baseURL, apiKey, modelName, isThinking, vendor, client)
			db.Exec(`UPDATE column_explanation SET is_machine_code = $1, note = $2, updated_at = now() WHERE id = $3`, cr.MC, cr.Reason, c.ID)
			if cr.MC {
				atomic.AddInt32(&machineCount, 1)
			}
			done := atomic.AddInt32(&doneCount, 1)
			if done%20 == 0 || int(done) == total {
				mc := int(atomic.LoadInt32(&machineCount))
				mu.Lock()
				log.Printf("PBIT: classified %d/%d columns, %d machine code so far", done, total, mc)
				if len(onProgress) > 0 && onProgress[0] != nil {
					onProgress[0](int(done), total, mc)
				}
				mu.Unlock()
			}
		}(col)
	}
	wg.Wait()

	mc := int(machineCount)
	log.Printf("PBIT: classification done — %d/%d columns are machine code", mc, total)
	return mc
}

// ClassifySingleColumn calls LLM for one column. Returns mc and reason.
func ClassifySingleColumn(col PbitColumnInfo, baseURL, apiKey, modelName string, isThinking bool, vendor string, client *http.Client, customPrompt ...string) ClassifyResult {
	samples := col.SampleValues
	if len(samples) > 20 {
		samples = samples[:20]
	}

	samplesStr := ""
	if len(samples) > 0 {
		b, _ := json.Marshal(samples)
		samplesStr = fmt.Sprintf("\n样本值: %s", string(b))
	}

	basePrompt := `判断以下 Power BI 列是否为"机器编码列"（系统生成的ID/编号/序列号，值无直接语义）。`
	if len(customPrompt) > 0 && customPrompt[0] != "" {
		basePrompt = customPrompt[0]
	}

	prompt := fmt.Sprintf(`%s

表: %s
列: %s
类型: %s%s

回复JSON格式：`+"```json"+`
{"mc":true或false,"reason":"简要原因"}
`+"```", basePrompt, col.TableName, col.ColumnName, col.DataType, samplesStr)

	messages := []M{{"role": "user", "content": prompt}}
	chatBody := M{"model": modelName, "messages": messages, "max_tokens": 512, "temperature": 0, "_vendor": vendor}

	if IsAnthropic(vendor) {
		content, _, err := doAnthropicChat(baseURL, apiKey, chatBody, "")
		if err != nil {
			log.Printf("PBIT: LLM call failed for %s.%s: %v", col.TableName, col.ColumnName, err)
			return ClassifyResult{MC: false, Reason: "LLM error"}
		}
		return parseClassifyResponse(content)
	}

	NormalizeMaxTokens(chatBody)
	delete(chatBody, "_vendor")
	reqBytes, _ := json.Marshal(chatBody)
	url := BuildURL(baseURL, "/chat/completions")
	req, _ := http.NewRequest("POST", url, bytes.NewReader(reqBytes))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("PBIT: LLM call failed for %s.%s: %v", col.TableName, col.ColumnName, err)
		return ClassifyResult{}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		io.Copy(io.Discard, resp.Body)
		return ClassifyResult{}
	}

	var chatResp M
	json.NewDecoder(resp.Body).Decode(&chatResp)

	content := ExtractChatContent(chatResp)
	return parseClassifyResponse(content)
}

func parseClassifyResponse(content string) ClassifyResult {
	content = StripThinkTags(content)
	content = ExtractJSON(content)

	var result struct {
		MC     bool   `json:"mc"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		lower := strings.ToLower(content)
		isMC := strings.Contains(lower, "\"mc\":true") || strings.Contains(lower, "\"mc\": true")
		return ClassifyResult{MC: isMC, Reason: ""}
	}
	return ClassifyResult{MC: result.MC, Reason: result.Reason}
}
