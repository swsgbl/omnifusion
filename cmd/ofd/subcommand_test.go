package main

import (
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/config"
)

// TestDispatchSubcommandUnknown 验证未知子命令显式报错而非静默进入 serve。
func TestDispatchSubcommandUnknown(t *testing.T) {
	cfg := &config.Config{}
	handled, err := dispatchSubcommand(cfg, "", []string{"definitely-not-a-command"})
	if !handled {
		t.Fatal("unknown subcommand must be handled")
	}
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("err = %v, want unknown subcommand error", err)
	}
}

// TestDispatchSubcommandEmptyArgs 空 args 必须返回未处理（进入 serve 由调用方决定）。
func TestDispatchSubcommandEmptyArgs(t *testing.T) {
	if handled, _ := dispatchSubcommand(&config.Config{}, "", nil); handled {
		t.Fatal("empty args must not be handled")
	}
}
