// catalogcmd.go 承载 `ofd catalog`（签名 feed 维护者工具面 +
// 社区众测证据报告）：
//
//	ofd catalog keygen <seed.hex> # 生成 ed25519 密钥对（seed 落文件，pub 打印）
//	ofd catalog sign <feed.json> <seed.hex> # 签名 → feed.json.sig
//	ofd catalog verify <feed.json> [sig] # 用配置公钥（或 --pubkey）验签+结构校验
//	ofd catalog report [--days N] # 众测证据：上次接受 feed 的条目 × request_log 聚合
package main

import (
	"crypto/ed25519"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/swsgbl/omnifusion/internal/catalogfeed"
	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/store"
)

// runCatalogCommand 分发 catalog 子命令。
func runCatalogCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: ofd catalog keygen|sign|verify|report")
	}
	switch args[0] {
	case "keygen":
		return runCatalogKeygen(args[1:])
	case "sign":
		return runCatalogSign(args[1:])
	case "verify":
		return runCatalogVerify(cfg, args[1:])
	case "report":
		return runCatalogReport(cfg, args[1:])
	}
	return fmt.Errorf("unknown catalog subcommand %q", args[0])
}

// runCatalogKeygen 生成密钥对：seed hex 写文件（妥存，签名用），
// 公钥 hex 打印（pin 进配置 catalog.feed_pubkey）。
func runCatalogKeygen(args []string) error {
	fs := flag.NewFlagSet("catalog keygen", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: ofd catalog keygen <seed-out.hex>")
	}
	seedHex, pubHex, err := catalogfeed.GenerateKey()
	if err != nil {
		return err
	}
	if err := os.WriteFile(fs.Arg(0), []byte(seedHex), 0o600); err != nil {
		return fmt.Errorf("write seed: %w", err)
	}
	fmt.Println(pubHex)
	return nil
}

// runCatalogSign 对 feed 原始字节签名，签名 hex 写 <feed>.sig。
func runCatalogSign(args []string) error {
	fs := flag.NewFlagSet("catalog sign", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: ofd catalog sign <feed.json> <seed.hex>")
	}
	feedPath, seedPath := fs.Arg(0), fs.Arg(1)
	raw, err := os.ReadFile(feedPath)
	if err != nil {
		return err
	}
	seed, err := os.ReadFile(seedPath)
	if err != nil {
		return err
	}
	sigHex, pubHex, err := catalogfeed.Sign(raw, string(seed))
	if err != nil {
		return err
	}
	sigPath := feedPath + ".sig"
	if err := os.WriteFile(sigPath, []byte(sigHex+"\n"), 0o644); err != nil {
		return fmt.Errorf("write signature: %w", err)
	}
	fmt.Printf("%s (pubkey %s)\n", sigPath, pubHex)
	return nil
}

// runCatalogVerify 验签 + 结构校验（离线检查 feed 完整性；公钥取
// --pubkey 或配置 catalog.feed_pubkey）。退出码非 0 = 不可信。
func runCatalogVerify(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("catalog verify", flag.ContinueOnError)
	pubHex := fs.String("pubkey", "", "ed25519 public key (64 hex; default: config catalog.feed_pubkey)")
	positional, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(positional) < 1 || len(positional) > 2 {
		return fmt.Errorf("usage: ofd catalog verify <feed.json> [feed.json.sig] --pubkey <hex>")
	}
	key := *pubHex
	if key == "" {
		key = cfg.Catalog.FeedPubkey
	}
	if key == "" {
		return fmt.Errorf("no public key: pass --pubkey or set catalog.feed_pubkey in config")
	}
	pub, err := catalogfeed.ParsePublicKey(key)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(positional[0])
	if err != nil {
		return err
	}
	sigPath := positional[0] + ".sig"
	if len(positional) == 2 {
		sigPath = positional[1]
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return err
	}
	if err := catalogfeed.Verify(raw, string(sig), pub); err != nil {
		return fmt.Errorf("feed NOT trusted: %w", err)
	}
	f, err := catalogfeed.ParseFeed(raw)
	if err != nil {
		return fmt.Errorf("feed NOT trusted: %w", err)
	}
	n := 0
	for _, pf := range f.Providers {
		n += len(pf.Models)
	}
	fmt.Printf("feed OK: version %d, %d providers, %d models, generated_at %s\n",
		f.Version, len(f.Providers), n, time.Unix(f.GeneratedAt, 0).UTC().Format(time.RFC3339))
	return nil
}

// reportRow 是众测证据报告的一行（feed 条目 × 流量证据）。
type reportRow struct {
	Provider  string
	Model     string
	Probation bool
	Calls     int64
	OK        int64
	Errors    int64
}

// catalogReport 计算报告数据：上次接受的 feed 条目（probation 标注）
// 联结 request_log 的逐模型证据窗口。
func catalogReport(cfg *config.Config, days int) ([]reportRow, error) {
	if days <= 0 {
		days = 7
	}
	st, err := store.Open(cfg.Store.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()

	raw := newIngestorForReport(st).LastFeed()
	if raw == nil {
		return nil, fmt.Errorf("no accepted feed in store; run the gateway with catalog.feed_url configured first")
	}
	f, err := catalogfeed.ParseFeed(raw)
	if err != nil {
		return nil, fmt.Errorf("stored feed unparseable: %w", err)
	}
	since := time.Now().AddDate(0, 0, -days).Unix()
	var rows []reportRow
	for name, pf := range f.Providers {
		ev, err := st.QueryModelEvidence(name, since)
		if err != nil {
			return nil, err
		}
		byModel := map[string]store.ModelEvidence{}
		for _, e := range ev {
			byModel[e.Model] = e
		}
		for _, m := range pf.Models {
			e := byModel[m.ID]
			rows = append(rows, reportRow{
				Provider: name, Model: m.ID,
				Probation: m.Status == catalogfeed.StatusProbation,
				Calls:     e.Calls, OK: e.OK, Errors: e.Errors,
			})
		}
	}
	return rows, nil
}

// newIngestorForReport 构造仅回读（无公钥/网络）的摄取器视图。
func newIngestorForReport(st *store.Store) *catalogfeed.Ingestor {
	return catalogfeed.NewIngestor(st, ed25519.PublicKey{}, nil)
}

// runCatalogReport 打印众测证据报告（feed 维护者裁决升降级的依据面）。
func runCatalogReport(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("catalog report", flag.ContinueOnError)
	days := fs.Int("days", 7, "evidence window in days")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rows, err := catalogReport(cfg, *days)
	if err != nil {
		return err
	}
	fmt.Printf("%-12s %-34s %-10s %6s %6s %6s\n",
		"PROVIDER", "MODEL", "STATUS", "CALLS", "OK", "ERR")
	for _, r := range rows {
		status := "stable"
		if r.Probation {
			status = "probation"
		}
		fmt.Printf("%-12s %-34s %-10s %6d %6d %6d\n",
			r.Provider, r.Model, status, r.Calls, r.OK, r.Errors)
	}
	fmt.Printf("\npublic key fingerprint check: seed file hex is the private key; " +
		"verify feeds with `ofd catalog verify`\n")
	return nil
}
