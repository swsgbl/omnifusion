package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/swsgbl/omnifusion/internal/config"
	"github.com/swsgbl/omnifusion/internal/provider/registry"
	"github.com/swsgbl/omnifusion/internal/security"
	"github.com/swsgbl/omnifusion/internal/store"
)

// keyFlagSet 声明 add 子命令的选项。
func keyFlagSet() (*flag.FlagSet, *string, *string, *bool) {
	fs := flag.NewFlagSet("key add", flag.ContinueOnError)
	label := fs.String("label", "", "key label for `ofd key list`")
	envVar := fs.String("env", "", "read the key from this environment variable")
	fromStdin := fs.Bool("stdin", false, "read the key from stdin")
	return fs, label, envVar, fromStdin
}

// parseInterleaved 支持旗标与位置参数任意交错（标准库 flag 遇首个
// 非旗标即停，这里循环续解析剩余段）。
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for len(rest) > 0 {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	return positional, nil
}

// runKeyCommand 处理 `ofd key add|list|remove`：
// 密钥经 AES-256-GCM 加密后落 SQLite connections 表，明文不出终端。
func runKeyCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 {
		return keyUsage()
	}
	switch args[0] {
	case "add":
		return keyAdd(cfg, args[1:])
	case "list", "ls":
		return keyList(cfg)
	case "remove", "rm":
		return keyRemove(cfg, args[1:])
	default:
		return keyUsage()
	}
}

func keyUsage() error {
	return errors.New(`usage:
  ofd key add <provider> [--label L] [--env VAR | --stdin]
  ofd key list
  ofd key remove <provider>`)
}

func keyAdd(cfg *config.Config, args []string) error {
	fs, label, envVar, fromStdin := keyFlagSet()
	positional, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return keyUsage()
	}
	providerID := positional[0]
	warnUnknownProvider(providerID)
	if *envVar != "" && *fromStdin {
		return errors.New("--env and --stdin are mutually exclusive")
	}

	key, err := obtainKey(providerID, *envVar, *fromStdin)
	if err != nil {
		return err
	}
	if key == "" {
		return errors.New("empty key; nothing stored")
	}

	st, kr, close, err := openKeyDeps(cfg)
	if err != nil {
		return err
	}
	defer close()

	ct, err := kr.Encrypt([]byte(key))
	if err != nil {
		return fmt.Errorf("encrypt key: %w", err)
	}
	if err := st.SetConnection(providerID, ct, *label); err != nil {
		return err
	}
	fmt.Printf("stored encrypted key for %q (label=%q)\n", providerID, *label)
	return nil
}

func keyList(cfg *config.Config) error {
	st, _, close, err := openKeyDeps(cfg)
	if err != nil {
		return err
	}
	defer close()

	conns, err := st.ListConnections()
	if err != nil {
		return err
	}
	if len(conns) == 0 {
		fmt.Println("no stored keys; add one with: ofd key add <provider>")
		return nil
	}
	for _, c := range conns {
		label := c.Label
		if label == "" {
			label = "-"
		}
		fmt.Printf("%-14s label=%-12s updated=%s\n", c.Provider, label, c.UpdatedAt)
	}
	return nil
}

func keyRemove(cfg *config.Config, args []string) error {
	if len(args) < 1 {
		return keyUsage()
	}
	st, _, close, err := openKeyDeps(cfg)
	if err != nil {
		return err
	}
	defer close()

	if err := st.DeleteConnection(args[0]); err != nil {
		return err
	}
	fmt.Printf("removed stored key for %q\n", args[0])
	return nil
}

// openKeyDeps 打开 store 与 keyring（主密钥派生自机器身份）。
func openKeyDeps(cfg *config.Config) (*store.Store, *security.Keyring, func(), error) {
	if dir := filepath.Dir(cfg.Store.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	st, err := openStore(cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open store: %w", err)
	}
	kr, err := security.Open("")
	if err != nil {
		_ = st.Close()
		return nil, nil, nil, fmt.Errorf("open keyring: %w", err)
	}
	return st, kr, func() { _ = st.Close() }, nil
}

// obtainKey 按 --env / --stdin / 交互隐藏输入的顺序取密钥。
func obtainKey(providerID, envVar string, fromStdin bool) (string, error) {
	switch {
	case envVar != "":
		v := os.Getenv(envVar)
		if v == "" {
			return "", fmt.Errorf("environment variable %s is empty", envVar)
		}
		return v, nil
	case fromStdin:
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		return strings.TrimSpace(line), nil
	default:
		return readKeyInteractive(providerID)
	}
}

// readKeyInteractive 在终端隐藏回显读取；非终端退化为读一行。
func readKeyInteractive(providerID string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		_, _ = fmt.Fprintf(os.Stderr, "Enter API key for %s (input hidden): ", providerID)
		b, err := term.ReadPassword(fd)
		_, _ = fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("read key: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// runGatewayKeyCommand 打印网关统一 API Key：派生自主密钥，
// 确定性、不落盘；客户端以 `Authorization: Bearer <key>` 访问数据面。
func runGatewayKeyCommand() error {
	kr, err := security.Open("")
	if err != nil {
		return fmt.Errorf("open keyring: %w", err)
	}
	token, err := kr.GatewayToken()
	if err != nil {
		return fmt.Errorf("derive gateway token: %w", err)
	}
	fmt.Println(token)
	return nil
}

// warnUnknownProvider 对注册表之外的 provider 给提示但不阻止
// （允许为后续自定义上游预存密钥）。
func warnUnknownProvider(providerID string) {
	entries, err := registry.Load()
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.ID == providerID {
			return
		}
	}
	_, _ = fmt.Fprintf(os.Stderr, "warning: %q is not a built-in provider; storing anyway\n", providerID)
}
