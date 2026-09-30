package catalogfeed

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/store"
)

// newIngestTestStore 开一个 TempDir SQLite（catalogfeed 包测试自用）。
func newIngestTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "feed.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// feedBytes 构造一个合法 feed（now 时刻生成，避免新鲜度误伤）。
func feedBytes(t *testing.T, version int64, genAt time.Time) []byte {
	t.Helper()
	return []byte(fmt.Sprintf(
		`{"version":%d,"generated_at":%d,"providers":{"demo":{"models":[{"id":"m-a","ctx_len":4096,"status":"stable"}]}}}`,
		version, genAt.Unix()))
}

// signedFeed 返回 raw 与其正确签名。
func signedFeed(t *testing.T, raw []byte, seed string) string {
	t.Helper()
	sig, _, err := Sign(raw, seed)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	return sig
}

func TestIngestAcceptsAndAdvancesBaseline(t *testing.T) {
	st := newIngestTestStore(t)
	seed, pub, _ := GenerateKey()
	g := NewIngestor(st, mustParsePub(t, pub), nil)
	now := time.Now()

	if g.Baseline() != 0 {
		t.Fatalf("initial baseline = %d, want 0", g.Baseline())
	}
	if g.LastFeed() != nil {
		t.Fatal("initial LastFeed not nil")
	}
	raw := feedBytes(t, 3, now.Add(-time.Minute))
	f, err := g.Ingest(raw, signedFeed(t, raw, seed))
	if err != nil {
		t.Fatalf("Ingest v3: %v", err)
	}
	if f.Version != 3 {
		t.Fatalf("feed version = %d, want 3", f.Version)
	}
	if g.Baseline() != 3 {
		t.Fatalf("baseline after v3 = %d, want 3", g.Baseline())
	}
	if string(g.LastFeed()) != string(raw) {
		t.Fatal("LastFeed mismatch with ingested raw")
	}

	// 基线持久化：新实例（模拟重启）读同一 store 仍拒旧版本。
	g2 := NewIngestor(st, mustParsePub(t, pub), nil)
	if g2.Baseline() != 3 {
		t.Fatalf("baseline after reopen = %d, want 3", g2.Baseline())
	}
	old := feedBytes(t, 2, now.Add(-time.Minute))
	_, err = g2.Ingest(old, signedFeed(t, old, seed))
	var rb *RollbackError
	if !errors.As(err, &rb) || rb.FeedVersion != 2 || rb.Baseline != 3 {
		t.Fatalf("v2 after v3: err = %v, want RollbackError{2,3}", err)
	}
}

func TestIngestRejectsReplay(t *testing.T) {
	st := newIngestTestStore(t)
	seed, pub, _ := GenerateKey()
	g := NewIngestor(st, mustParsePub(t, pub), nil)
	now := time.Now()

	raw := feedBytes(t, 5, now.Add(-time.Minute))
	if _, err := g.Ingest(raw, signedFeed(t, raw, seed)); err != nil {
		t.Fatalf("Ingest v5: %v", err)
	}
	_, err := g.Ingest(raw, signedFeed(t, raw, seed))
	var rb *RollbackError
	if !errors.As(err, &rb) || rb.FeedVersion != 5 || rb.Baseline != 5 {
		t.Fatalf("replay v5: err = %v, want RollbackError{5,5}", err)
	}
}

func TestIngestRejectsBadSignature(t *testing.T) {
	st := newIngestTestStore(t)
	_, pub, _ := GenerateKey()       // pinned 公钥
	wrongSeed, _, _ := GenerateKey() // 签名用的另一对
	g := NewIngestor(st, mustParsePub(t, pub), nil)
	raw := feedBytes(t, 1, time.Now().Add(-time.Minute))
	if _, err := g.Ingest(raw, signedFeed(t, raw, wrongSeed)); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("wrong-key sig: err = %v, want ErrBadSignature", err)
	}
	if g.Baseline() != 0 {
		t.Fatalf("baseline advanced on bad sig: %d", g.Baseline())
	}
}

func TestIngestRejectsFutureTimestamp(t *testing.T) {
	st := newIngestTestStore(t)
	seed, pub, _ := GenerateKey()
	g := NewIngestor(st, mustParsePub(t, pub), nil)
	raw := feedBytes(t, 1, time.Now().Add(MaxClockSkew+time.Hour))
	if _, err := g.Ingest(raw, signedFeed(t, raw, seed)); err == nil {
		t.Fatal("future generated_at accepted")
	}
	if g.Baseline() != 0 {
		t.Fatalf("baseline advanced on stale-reject: %d", g.Baseline())
	}
}

// TestIngestRejectsSignatureTransplant 是签名移植利用回归（蓝图
// Phase 8 §九）：把 v3 的合法签名嫁接到 v4 字节上（恶意镜像改版本号
// 后原样转发旧签名）——验签对字节，移植必拒，基线不推进。
func TestIngestRejectsSignatureTransplant(t *testing.T) {
	st := newIngestTestStore(t)
	seed, pub, _ := GenerateKey()
	g := NewIngestor(st, mustParsePub(t, pub), nil)
	now := time.Now()

	v3 := feedBytes(t, 3, now.Add(-time.Minute))
	if _, err := g.Ingest(v3, signedFeed(t, v3, seed)); err != nil {
		t.Fatalf("seed v3: %v", err)
	}
	v4 := feedBytes(t, 4, now.Add(-time.Minute))
	if _, err := g.Ingest(v4, signedFeed(t, v3, seed)); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("transplanted v3 sig on v4 bytes: err = %v, want ErrBadSignature", err)
	}
	if g.Baseline() != 3 {
		t.Fatalf("baseline advanced on transplant: %d", g.Baseline())
	}
}

// TestIngestRejectsExpiredFeed 是过期行为回归（蓝图 Phase 8 §九
// "expiry"）：生成时刻超过 MaxFeedAge 的 feed（恶意镜像重放归档）
// 拒收且基线不动；30~90 天之间接受但告警（捕获日志断言）。
func TestIngestRejectsExpiredFeed(t *testing.T) {
	st := newIngestTestStore(t)
	seed, pub, _ := GenerateKey()
	g := NewIngestor(st, mustParsePub(t, pub), nil)
	now := time.Now()

	old := feedBytes(t, 9, now.Add(-MaxFeedAge-time.Hour))
	if _, err := g.Ingest(old, signedFeed(t, old, seed)); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Fatalf("archived feed accepted: err = %v, want expired error", err)
	}
	if g.Baseline() != 0 {
		t.Fatalf("baseline advanced on expired feed: %d", g.Baseline())
	}

	// 40 天旧：接受 + 陈旧告警。
	var buf strings.Builder
	gw := NewIngestor(st, mustParsePub(t, pub), slog.New(slog.NewTextHandler(&buf, nil)))
	stale := feedBytes(t, 10, now.Add(-40*24*time.Hour))
	if _, err := gw.Ingest(stale, signedFeed(t, stale, seed)); err != nil {
		t.Fatalf("stale-but-not-expired feed must be accepted: %v", err)
	}
	if gw.Baseline() != 10 {
		t.Fatalf("baseline after stale accept = %d, want 10", gw.Baseline())
	}
	if !strings.Contains(buf.String(), "catalog feed is stale") {
		t.Fatalf("stale warning missing: %s", buf.String())
	}
}

func TestFetchAndRefresh(t *testing.T) {
	st := newIngestTestStore(t)
	seed, pub, _ := GenerateKey()
	g := NewIngestor(st, mustParsePub(t, pub), nil)
	raw := feedBytes(t, 7, time.Now().Add(-time.Minute))
	sig := signedFeed(t, raw, seed)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(SignatureHeader, sig)
		w.Write(raw)
	}))
	t.Cleanup(srv.Close)

	gotRaw, gotSig, err := g.Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(gotRaw) != string(raw) || gotSig != sig {
		t.Fatal("Fetch body/signature mismatch")
	}
	f, err := g.Refresh(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if f.Version != 7 || g.Baseline() != 7 {
		t.Fatalf("after Refresh: version %d baseline %d, want 7/7", f.Version, g.Baseline())
	}
}

func TestFetchRejects(t *testing.T) {
	_, pub, _ := GenerateKey()
	g := NewIngestor(nil, mustParsePub(t, pub), nil)

	// 缺签名头。
	noSig := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("{}"))
	}))
	t.Cleanup(noSig.Close)
	if _, _, err := g.Fetch(context.Background(), noSig.URL); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing sig header: err = %v, want missing", err)
	}

	// 非 200。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(bad.Close)
	if _, _, err := g.Fetch(context.Background(), bad.URL); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("404: err = %v, want status 404", err)
	}
}

// mustParsePub 解析 pinned 公钥，失败即测试终止。
func mustParsePub(t *testing.T, pubHex string) ed25519.PublicKey {
	t.Helper()
	pub, err := ParsePublicKey(pubHex)
	if err != nil {
		t.Fatalf("ParsePublicKey: %v", err)
	}
	return pub
}
