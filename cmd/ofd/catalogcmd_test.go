package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/catalogfeed"
	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/store"
)

// TestCatalogKeygenSignVerify 是 维护者工具面往返验收：
// keygen 出 seed → sign 出 detached 签名 → verify 验签+结构校验。
func TestCatalogKeygenSignVerify(t *testing.T) {
	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.hex")

	if err := runCatalogKeygen([]string{seedPath}); err != nil {
		t.Fatalf("keygen: %v", err)
	}
	seedBytes, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	seed := strings.TrimSpace(string(seedBytes))
	if len(seed) != 64 {
		t.Fatalf("seed length %d, want 64", len(seed))
	}

	feedJSON := `{"version":4,"generated_at":` +
		itos(time.Now().Add(-time.Minute).Unix()) +
		`,"providers":{"demo":{"models":[{"id":"m-a","ctx_len":4096,"status":"probation"}]}}}`
	feedPath := filepath.Join(dir, "feed.json")
	if err := os.WriteFile(feedPath, []byte(feedJSON), 0o644); err != nil {
		t.Fatalf("write feed: %v", err)
	}
	if err := runCatalogSign([]string{feedPath, seedPath}); err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig, err := os.ReadFile(feedPath + ".sig")
	if err != nil {
		t.Fatalf("read sig: %v", err)
	}
	if len(strings.TrimSpace(string(sig))) != 128 {
		t.Fatalf("sig length %d, want 128", len(strings.TrimSpace(string(sig))))
	}

	_, pubHex, err := catalogfeed.Sign([]byte(feedJSON), seed)
	if err != nil {
		t.Fatalf("derive pubkey: %v", err)
	}
	cfg := &config.Config{}
	if err := runCatalogVerify(cfg, []string{feedPath, "--pubkey", pubHex}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// 配置公钥路径也走通。
	cfg.Catalog.FeedPubkey = pubHex
	if err := runCatalogVerify(cfg, []string{feedPath}); err != nil {
		t.Fatalf("verify with config pubkey: %v", err)
	}
	// 坏公钥必须拒。
	bad := strings.Repeat("ab", 32)
	if err := runCatalogVerify(cfg, []string{feedPath, "--pubkey", bad}); err == nil {
		t.Fatal("verify with wrong pubkey accepted")
	}
}

// TestCatalogReportEvidence 是 众测报告验收：feed 条目（probation
// 标注）× request_log 聚合证据联结成行。
func TestCatalogReportEvidence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "report.db")
	cfg := &config.Config{}
	cfg.Store.Path = dbPath

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	seed, pub, _ := catalogfeed.GenerateKey()
	parsed, err := catalogfeed.ParsePublicKey(pub)
	if err != nil {
		t.Fatalf("ParsePublicKey: %v", err)
	}
	now := time.Now().Unix()
	feedJSON := `{"version":2,"generated_at":` + itos(now-60) +
		`,"providers":{"demo":{"models":[{"id":"m-a","ctx_len":8192,"status":"stable"},` +
		`{"id":"m-b","ctx_len":4096,"status":"probation"}]}}}`
	sig, _, err := catalogfeed.Sign([]byte(feedJSON), seed)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	ing := catalogfeed.NewIngestor(st, parsed, nil)
	if _, err := ing.Ingest([]byte(feedJSON), sig); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	logs := []store.RequestLog{
		{TS: now - 10, Provider: "demo", Model: "m-a", Status: 200},
		{TS: now - 20, Provider: "demo", Model: "m-a", Status: 200},
		{TS: now - 30, Provider: "demo", Model: "m-a", Status: 429},
		{TS: now - 40, Provider: "demo", Model: "m-b", Status: 200},
	}
	for i, r := range logs {
		if err := st.InsertRequestLog(r); err != nil {
			t.Fatalf("InsertRequestLog[%d]: %v", i, err)
		}
	}
	st.Close()

	rows, err := catalogReport(cfg, 7)
	if err != nil {
		t.Fatalf("catalogReport: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rows)
	}
	byModel := map[string]reportRow{}
	for _, r := range rows {
		byModel[r.Model] = r
	}
	ra := byModel["m-a"]
	if ra.Provider != "demo" || ra.Probation || ra.Calls != 3 || ra.OK != 2 || ra.Errors != 1 {
		t.Errorf("m-a row = %+v, want demo/stable/3/2/1", ra)
	}
	rb := byModel["m-b"]
	if !rb.Probation || rb.Calls != 1 || rb.OK != 1 || rb.Errors != 0 {
		t.Errorf("m-b row = %+v, want probation/1/1/0", rb)
	}
	if err := runCatalogReport(cfg, []string{}); err != nil {
		t.Fatalf("runCatalogReport: %v", err)
	}
}

func itos(n int64) string { return strconv.FormatInt(n, 10) }
