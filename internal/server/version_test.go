package server

import "testing"

func TestSetVersion(t *testing.T) {
	old := version
	defer func() { version = old }()

	SetVersion("")
	if version != old {
		t.Fatalf("empty version must not overwrite (got %q)", version)
	}
	SetVersion("v0.1.0")
	if version != "v0.1.0" {
		t.Fatalf("want v0.1.0, got %q", version)
	}
	SetVersion("dev") // 注入后不得回退（源码默认值防抖）
	if version != "v0.1.0" {
		t.Fatalf("dev must not overwrite an injected version (got %q)", version)
	}
}
