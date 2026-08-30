// storepath.go 是每用户规范数据路径的落点（store.Open 的统一入口）：
// ① 首次运行时若规范库尚不存在而历史遗留库存在（旧默认跟随工作目录的
// data/omnifusion.db），自动把最新的遗留库迁入规范位置——密钥/隔离/
// 缓存无缝延续，用户无感；② 规范库已存在则幂等跳过。显式配置
// store.path 的用户不走迁移（自家路径自家管）。
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/store"
)

// openStore 迁移遗留库（如需）后打开规范存储。全部子命令经此入口，
// 不存在半边迁移。
func openStore(cfg *config.Config) (*store.Store, error) {
	migrateLegacyStore(cfg.Store.Path)
	return store.Open(cfg.Store.Path)
}

// migrateLegacyStore 把旧默认位置（跟随工作目录的 ./data 或 exe 旁的
// data）中最新的 omnifusion.db 迁入规范路径——仅在规范库不存在时执行
// 一次；连 -wal/-shm 一起搬（冷拷贝，无并发写者时安全）。
func migrateLegacyStore(canonical string) {
	if _, err := os.Stat(canonical); err == nil {
		return // 正本已在：幂等
	}
	candidates := []string{
		filepath.Join("data", "omnifusion.db"), // 旧默认：跟随工作目录
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "data", "omnifusion.db"))
	}
	var src string
	var srcMod int64
	for _, c := range candidates {
		if c == canonical {
			continue
		}
		info, err := os.Stat(c)
		if err != nil || info.IsDir() {
			continue
		}
		if src == "" || info.ModTime().UnixNano() > srcMod {
			src, srcMod = c, info.ModTime().UnixNano()
		}
	}
	if src == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		fmt.Printf("store: migrate skip (mkdir: %v)\n", err)
		return
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		in, out := src+suffix, canonical+suffix
		b, err := os.ReadFile(in)
		if err != nil {
			continue
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			fmt.Printf("store: migrate skip (write %s: %v)\n", out, err)
			return
		}
	}
	fmt.Printf("store: migrated legacy %s -> %s (single source of truth)\n", src, canonical)
}
