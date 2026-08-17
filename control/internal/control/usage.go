package control

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Token/model usage, aggregated from the Claude Code JSONL transcripts each
// agent's sessions write under ~/.claude/projects/<escaped-cwd>/*.jsonl. We
// read ONLY the numeric usage + model + timestamp off each assistant turn —
// never message content — and bucket it by day / model / agent. Results are
// cached briefly since a full scan touches many files.

const (
	usageWindowDays = 14
	usageCacheTTL   = 45 * time.Second
)

type usageSums struct {
	In          int64   `json:"in"`
	Out         int64   `json:"out"`
	CacheRead   int64   `json:"cacheRead"`
	CacheCreate int64   `json:"cacheCreate"`
	Total       int64   `json:"total"`
	Messages    int64   `json:"messages"`
	CostUSD     float64 `json:"costUSD"` // API-equivalent $ (governor's CostRatesPerMTok), per model
}

// add folds one assistant turn in. model drives the API-equivalent cost via the
// governor's single rate table, so Usage and the budget block agree on units.
func (s *usageSums) add(in, out, cr, cc int64, model string) {
	s.In += in
	s.Out += out
	s.CacheRead += cr
	s.CacheCreate += cc
	s.Total += in + out + cr + cc
	s.CostUSD += usageCostUSD(in, out, cc, cr, model)
	s.Messages++
}

type dayUsage struct {
	Date string `json:"date"` // YYYY-MM-DD (Asia/Bishkek)
	usageSums
}

type modelUsage struct {
	Model string `json:"model"`
	usageSums
}

type agentUsage struct {
	Agent string `json:"agent"`
	usageSums
}

type usageResp struct {
	WindowDays int          `json:"windowDays"`
	Generated  int64        `json:"generated"`
	Totals     usageSums    `json:"totals"`
	Days       []dayUsage   `json:"days"`   // ascending, every day in the window (zero-filled)
	Models     []modelUsage `json:"models"` // desc by total
	Agents     []agentUsage `json:"agents"` // desc by total
}

// minimal shape decoded per JSONL line — unknown fields (incl. message.content)
// are skipped by the decoder, so we never materialise transcript content.
type usageLine struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Message   struct {
		Model string `json:"model"`
		Usage struct {
			In          int64 `json:"input_tokens"`
			Out         int64 `json:"output_tokens"`
			CacheCreate int64 `json:"cache_creation_input_tokens"`
			CacheRead   int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

var (
	usageMu     sync.Mutex
	usageCache  *usageResp
	usageCached time.Time
)

// projectDir maps a company/agent to its Claude Code transcript directory. The
// project dir name is the cwd with '/' and '.' replaced by '-'.
func projectDir(agent string) string {
	cwd := filepath.Join(home, "workspace", agent)
	esc := strings.NewReplacer("/", "-", ".", "-").Replace(cwd)
	return filepath.Join(home, ".claude", "projects", esc)
}

func computeUsage() *usageResp {
	loc := bishkek
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	start := today.AddDate(0, 0, -(usageWindowDays - 1))
	cutoff := start.Add(-24 * time.Hour) // mtime prefilter slack

	dayIdx := map[string]*dayUsage{}
	days := make([]dayUsage, usageWindowDays)
	for i := range days {
		d := start.AddDate(0, 0, i)
		days[i].Date = d.Format("2006-01-02")
	}
	// second pass wires pointers after slice is stable
	for i := range days {
		dayIdx[days[i].Date] = &days[i]
	}

	models := map[string]*modelUsage{}
	agents := map[string]*agentUsage{}
	var totals usageSums

	consume := func(agent string, l *usageLine) {
		if l.Type != "assistant" || l.Message.Model == "" {
			return
		}
		u := l.Message.Usage
		if u.In == 0 && u.Out == 0 && u.CacheRead == 0 && u.CacheCreate == 0 {
			return
		}
		ts := l.Timestamp.In(loc)
		if ts.Before(start) {
			return
		}
		model := l.Message.Model
		date := ts.Format("2006-01-02")
		if d := dayIdx[date]; d != nil {
			d.add(u.In, u.Out, u.CacheRead, u.CacheCreate, model)
		}
		mk := shortModelName(model)
		m := models[mk]
		if m == nil {
			m = &modelUsage{Model: mk}
			models[mk] = m
		}
		m.add(u.In, u.Out, u.CacheRead, u.CacheCreate, model)
		a := agents[agent]
		if a == nil {
			a = &agentUsage{Agent: agent}
			agents[agent] = a
		}
		a.add(u.In, u.Out, u.CacheRead, u.CacheCreate, model)
		totals.add(u.In, u.Out, u.CacheRead, u.CacheCreate, model)
	}

	for _, agent := range companies {
		dir := projectDir(agent)
		files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		for _, f := range files {
			if st, err := os.Stat(f); err != nil || st.ModTime().Before(cutoff) {
				continue // untouched during the window -> no recent turns
			}
			scanFile(f, agent, consume)
		}
	}

	modelList := make([]modelUsage, 0, len(models))
	for _, m := range models {
		modelList = append(modelList, *m)
	}
	sort.Slice(modelList, func(i, j int) bool { return modelList[i].Total > modelList[j].Total })
	agentList := make([]agentUsage, 0, len(agents))
	for _, a := range agents {
		agentList = append(agentList, *a)
	}
	sort.Slice(agentList, func(i, j int) bool { return agentList[i].Total > agentList[j].Total })

	return &usageResp{
		WindowDays: usageWindowDays,
		Generated:  time.Now().Unix(),
		Totals:     totals,
		Days:       days,
		Models:     modelList,
		Agents:     agentList,
	}
}

func scanFile(path, agent string, consume func(string, *usageLine)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReaderSize(f, 1<<16))
	for {
		var l usageLine
		if err := dec.Decode(&l); err != nil {
			if err == io.EOF {
				return
			}
			return // malformed tail — stop this file, keep what we have
		}
		consume(agent, &l)
	}
}

func shortModelName(m string) string {
	low := strings.ToLower(m)
	for _, k := range []string{"opus", "sonnet", "haiku", "fable"} {
		if strings.Contains(low, k) {
			return k
		}
	}
	return low
}

func handleUsage(w http.ResponseWriter, _ *http.Request) {
	usageMu.Lock()
	if usageCache == nil || time.Since(usageCached) > usageCacheTTL {
		usageCache = computeUsage()
		usageCached = time.Now()
	}
	resp := usageCache
	usageMu.Unlock()
	writeJSON(w, resp)
}
