package store

import "fmt"

// migrations 是有序的迁移脚本；追加新表时只能往末尾加，不许修改历史项。
// 后续里程碑的表结构见 。
var migrations = []string{
	// v1: 元数据表 —— 验证迁移机制的最小表
	`CREATE TABLE meta (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
	// v2: BYOK 连接——密钥只存 AES-256-GCM 密文（R5 对策 3）。
	`CREATE TABLE connections (
		provider   TEXT PRIMARY KEY,
		key_cipher BLOB NOT NULL,
		label      TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`,
	// v3: 三层隔离状态（ cooldowns 表）——重启不丢
	//（FreeRide 教训：冷却状态丢失 = 冷启动再踩一轮 429）。
	`CREATE TABLE cooldowns (
		scope_type TEXT NOT NULL CHECK (scope_type IN ('connection','model')),
		provider   TEXT NOT NULL,
		model      TEXT NOT NULL DEFAULT '',
		until_unix INTEGER NOT NULL,
		reason     TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (scope_type, provider, model)
	)`,
	// v4: 模型目录（ models 表）——catalog sync 的持久化
	// 目标：校验和判变更，变更才整组替换。
	`CREATE TABLE models (
		provider   TEXT NOT NULL,
		id         TEXT NOT NULL,
		ctx_len    INTEGER NOT NULL DEFAULT 0,
		free_meta  TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (provider, id)
	)`,
	// v5: 语义缓存（ semantic_cache 表）—— 精确层：
	// hash 主键（CacheKey = 确定性序列化请求的 SHA-256）、payload 存
	// 序列化的 UnifiedResponse、ts 为写入时刻（TTL 判定）。
	// embedding_blob 为 近似层（sqlite-vec，opt-in）预留，v1 恒 NULL。
	`CREATE TABLE semantic_cache (
		hash           TEXT PRIMARY KEY,
		payload        BLOB NOT NULL,
		embedding_blob BLOB,
		ts             INTEGER NOT NULL
	)`,
	// v6: 请求审计日志（ request_log 表）——每次数据面
	// 请求一行（含护栏拦截），查询走 ts 倒序索引；strategy 列以
	// endpoint+combo 口径落地（路由策略是内部状态）。ttft_ms<0 表非流式。
	`CREATE TABLE request_log (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		ts         INTEGER NOT NULL,
		endpoint   TEXT NOT NULL,
		model      TEXT NOT NULL DEFAULT '',
		provider   TEXT NOT NULL DEFAULT '',
		status     INTEGER NOT NULL,
		tokens_in  INTEGER NOT NULL DEFAULT 0,
		tokens_out INTEGER NOT NULL DEFAULT 0,
		latency_ms REAL NOT NULL DEFAULT 0,
		ttft_ms    REAL NOT NULL DEFAULT -1,
		cache_hit  INTEGER NOT NULL DEFAULT 0,
		error_kind TEXT NOT NULL DEFAULT '',
		combo      TEXT NOT NULL DEFAULT ''
	)`,
	// v7: 审计查询索引（ts 倒序扫描面）。
	`CREATE INDEX idx_request_log_ts ON request_log (ts)`,
	// v8: 会话记忆——FTS5 全文检索。unicode61 分词
	// 把 CJK 连续串切成单 token（中文词检索失效），故被索引列 tokens
	// 存调用方预处理的「拉丁小写词 + CJK bigram」空格串，原文存
	// UNINDEXED 列 content；写入/查询两侧走同一预处理
	//（intelligence.memoryTokens）保证词典一致——bigram 短语在
	// unicode61 下等价相邻单字短语匹配，中文检索由此生效。
	`CREATE VIRTUAL TABLE session_memory USING fts5(
		session_id   UNINDEXED,
		role         UNINDEXED,
		content      UNINDEXED,
		tokens,
		created_unix UNINDEXED,
		tokenize = 'unicode61'
	)`,
	// v9: 权益账本（蓝图 Phase 3 持久化切片）——
	// (provider, model) 主键的 Entitlement 快照。observed_at/valid_until
	// 存 RFC3339（账本对象原生 time.Time 往返）。remaining 存
	// [0,1] 比例，-1 表未知（SQL NULL→Go 侧回填 -1）。
	`CREATE TABLE entitlements (
		provider       TEXT NOT NULL,
		model          TEXT NOT NULL DEFAULT '',
		state          TEXT NOT NULL CHECK (state IN ('UNKNOWN','VERIFIED_FREE','VERIFIED_PAID','EXPIRED','DISABLED')),
		source         TEXT NOT NULL,
		evidence_id    TEXT NOT NULL DEFAULT '',
		terms_url      TEXT NOT NULL DEFAULT '',
		rpm            INTEGER NOT NULL DEFAULT 0,
		tpm            INTEGER NOT NULL DEFAULT 0,
		rpd            INTEGER NOT NULL DEFAULT 0,
		tpd            INTEGER NOT NULL DEFAULT 0,
		remaining      REAL,
		observed_at    TEXT NOT NULL DEFAULT '',
		valid_until    TEXT NOT NULL DEFAULT '',
		confidence     REAL NOT NULL DEFAULT 0,
		catalog_version TEXT NOT NULL DEFAULT '',
		updated_at     TEXT NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (provider, model)
	)`,
	// v10: 任务存储（蓝图 Phase 6 Tasks 扩展持久化切片）——
	// id 主键的 Task 快照（MCP tasks/get/update/cancel + 后续 A2A
	// 任务面的状态正本）。created_at/updated_at/deadline 存 RFC3339
	//（Task 对象原生 time.Time 往返；空串=零值/无截止）。每次状态
	// 迁移整行 upsert；重启 LoadFrom 恢复（僵尸 running→failed、
	// 过期→timed_out 的改写同样落库）。
	`CREATE TABLE tasks (
		id              TEXT PRIMARY KEY,
		kind            TEXT NOT NULL,
		status          TEXT NOT NULL CHECK (status IN ('created','running','completed','failed','canceled','timed_out')),
		created_at      TEXT NOT NULL DEFAULT '',
		updated_at      TEXT NOT NULL DEFAULT '',
		deadline        TEXT NOT NULL DEFAULT '',
		result          TEXT NOT NULL DEFAULT '',
		error           TEXT NOT NULL DEFAULT '',
		idempotency_key TEXT NOT NULL DEFAULT ''
	)`,
}

// migrate 幂等执行未应用的迁移（每条在事务中执行并记账）。
func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for i, script := range migrations {
		version := i + 1
		var applied int
		if err := s.db.QueryRow(
			`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version,
		).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if applied > 0 {
			continue
		}
		if err := s.applyMigration(version, script); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(version int, script string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", version, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(script); err != nil {
		return fmt.Errorf("apply migration %d: %w", version, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations (version) VALUES (?)`, version,
	); err != nil {
		return fmt.Errorf("record migration %d: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", version, err)
	}
	return nil
}
