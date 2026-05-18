package lakehouse

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Deterministic period parsing.
//
// Why this exists: the LLM fills period-style filter values with whatever the
// user typed — "2025年12月", "本月", "2025-12", "2025 Q4". Left as a raw filter
// value against a timestamp/date column those produce a text ILIKE that
// silently matches zero rows (the "static zero" failure: SQL succeeds, returns
// one row, value is 0, every auto-check passes). Expanding the expression into
// an explicit half-open range [start, end) makes the query deterministic
// regardless of how the LLM formatted the date.
//
// expandPeriod is the entry point used by resolve.go; normalizePeriod is the
// now-injected core kept separate for testability.

// expandPeriod parses a human period expression into a half-open date range
// [start, end) as ISO date strings. ok=false means the input is not a
// recognisable period and the caller should leave the original filter intact.
func expandPeriod(raw string) (start, end string, ok bool) {
	return normalizePeriod(raw, time.Now())
}

var (
	reMonth    = regexp.MustCompile(`^(\d{4})[-/.年](\d{1,2})月?$`)
	reDay      = regexp.MustCompile(`^(\d{4})[-/.年](\d{1,2})[-/.月](\d{1,2})日?$`)
	reYear     = regexp.MustCompile(`^(\d{4})年?$`)
	reQuarter  = regexp.MustCompile(`^(\d{4})[-/.年]?第?([1-4])季度?$`)
	reQuarterQ = regexp.MustCompile(`^(\d{4})[-/.]?[QqＱ]([1-4])$`)
)

// cnQuarter maps Chinese numeral quarters to their index.
var cnQuarter = map[string]int{"一": 1, "二": 2, "三": 3, "四": 4}

func normalizePeriod(raw string, now time.Time) (start, end string, ok bool) {
	s := strings.NewReplacer(" ", "", "　", "").Replace(strings.TrimSpace(raw))
	if s == "" {
		return "", "", false
	}

	// Relative expressions (resolved against now).
	switch s {
	case "本月", "这个月", "当月", "本月份":
		return monthRange(now.Year(), int(now.Month()))
	case "上月", "上个月", "上一个月", "前一个月":
		y, m := now.Year(), int(now.Month())-1
		if m == 0 {
			y, m = y-1, 12
		}
		return monthRange(y, m)
	case "今年", "本年", "本年度":
		return yearRange(now.Year())
	case "去年", "上一年", "上年":
		return yearRange(now.Year() - 1)
	}

	// Chinese-numeral quarter: 2025年第四季度 / 2025年四季度
	if mt := regexp.MustCompile(`^(\d{4})年第?([一二三四])季度?$`).FindStringSubmatch(s); mt != nil {
		y, _ := strconv.Atoi(mt[1])
		return quarterRange(y, cnQuarter[mt[2]])
	}
	// Arabic quarter: 2025年第4季度 / 2025-4季度 / 2025Q4 / 2025-Q4
	if mt := reQuarter.FindStringSubmatch(s); mt != nil {
		y, _ := strconv.Atoi(mt[1])
		q, _ := strconv.Atoi(mt[2])
		return quarterRange(y, q)
	}
	if mt := reQuarterQ.FindStringSubmatch(s); mt != nil {
		y, _ := strconv.Atoi(mt[1])
		q, _ := strconv.Atoi(mt[2])
		return quarterRange(y, q)
	}

	// Full day: 2025-12-01 / 2025年12月1日
	if mt := reDay.FindStringSubmatch(s); mt != nil {
		y, _ := strconv.Atoi(mt[1])
		mo, _ := strconv.Atoi(mt[2])
		d, _ := strconv.Atoi(mt[3])
		if mo >= 1 && mo <= 12 && d >= 1 && d <= 31 {
			st := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
			return st.Format("2006-01-02"), st.AddDate(0, 0, 1).Format("2006-01-02"), true
		}
		return "", "", false
	}

	// Month: 2025-12 / 2025/12 / 2025.12 / 2025年12月 / 2025-12月
	if mt := reMonth.FindStringSubmatch(s); mt != nil {
		y, _ := strconv.Atoi(mt[1])
		m, _ := strconv.Atoi(mt[2])
		return monthRange(y, m)
	}

	// Year only: 2025 / 2025年
	if mt := reYear.FindStringSubmatch(s); mt != nil {
		y, _ := strconv.Atoi(mt[1])
		return yearRange(y)
	}

	return "", "", false
}

func monthRange(y, m int) (string, string, bool) {
	if m < 1 || m > 12 {
		return "", "", false
	}
	st := time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC)
	return st.Format("2006-01-02"), st.AddDate(0, 1, 0).Format("2006-01-02"), true
}

func yearRange(y int) (string, string, bool) {
	st := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
	return st.Format("2006-01-02"), st.AddDate(1, 0, 0).Format("2006-01-02"), true
}

func quarterRange(y, q int) (string, string, bool) {
	if q < 1 || q > 4 {
		return "", "", false
	}
	st := time.Date(y, time.Month((q-1)*3+1), 1, 0, 0, 0, 0, time.UTC)
	return st.Format("2006-01-02"), st.AddDate(0, 3, 0).Format("2006-01-02"), true
}
