// toolfilter_diff_test.go 覆盖统一 diff 的形状感知压缩：文件名/hunk
// 头/有界窗口/每文件计数/显式截断标记。
package compression

import (
	"strings"
	"testing"
)

func TestCompactGitDiffKeepsShape(t *testing.T) {
	// 两个文件：第二个 hunk 超窗口（构造 120 行新增）。
	var big strings.Builder
	big.WriteString("diff --git a/main.go b/main.go\n")
	big.WriteString("--- a/main.go\n+++ b/main.go\n")
	big.WriteString("@@ -10,3 +10,4 @@ func main() {\n")
	big.WriteString(" context line\n")
	big.WriteString("+added line one\n")
	big.WriteString("+added line two\n")
	big.WriteString(" more context\n")
	big.WriteString("diff --git a/util.go b/util.go\n")
	big.WriteString("--- a/util.go\n+++ b/util.go\n")
	big.WriteString("@@ -1,1 +1,120 @@\n")
	for i := 0; i < 120; i++ {
		big.WriteString("+bulk ")
		big.WriteString(strings.Repeat("x", 8))
		big.WriteString("\n")
	}

	out := compactGitDiff(big.String())
	for _, want := range []string{
		"main.go",          // 文件名保留
		"util.go",          // 第二个文件名保留
		"@@ -10,3 +10,4 @@", // hunk 头保留
		"+added line one",  // 窗口内增行保留
		"+bulk",            // 第二 hunk 窗口内行保留
		"lines truncated",  // 超窗折叠标记
		"+2 -0",            // main.go 计数（context 不计入）
		"+120 -0",          // util.go 全量计数（窗口外也计数）
		"[diff compacted",  // 截断显式标记
	} {
		if !strings.Contains(out, want) {
			t.Errorf("compacted diff missing %q\nout:\n%s", want, out)
		}
	}
	if strings.Contains(out, "+++ b/main.go") {
		t.Errorf("file header line should be dropped: %s", out)
	}
	if got := len(strings.Split(out, "\n")); got > gitDiffMaxLines+8 {
		t.Errorf("output lines = %d, want bounded", got)
	}
}

func TestCompactGitDiffSmallDiffUnchanged(t *testing.T) {
	small := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,2 @@\n-a\n+b\n c\n"
	out := compactGitDiff(small)
	if !strings.Contains(out, "x.go") || !strings.Contains(out, "+b") || !strings.Contains(out, "+1 -1") {
		t.Errorf("small diff lost content: %s", out)
	}
	if strings.Contains(out, "truncated") {
		t.Errorf("small diff should not claim truncation: %s", out)
	}
}

func TestToolFilterStageNeverGrows(t *testing.T) {
	// 小 diff：压缩件不比原文短时整体保留原文（不放大规则）。
	small := "diff --git a/x.go b/x.go\n@@ -1,1 +1,2 @@\n-a\n+b\n"
	in := toolSession("git diff", small)
	st := NewToolFilterStage(ToolFilterConfig{MinTokens: 1, RecencyGuard: 2, MinOutputChars: 8, HeadLines: 4, TailLines: 2})
	out, _, err := st.Apply(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := singleTextOf(out[2]); len(got) > len(small) {
		t.Errorf("output must never grow: %d > %d (%q)", len(got), len(small), got)
	}
}

func TestToolFilterStageUsesDiffCompaction(t *testing.T) {
	// 走 stage 入口：160 行新增超 100 行 hunk 窗口 → 真实折叠生效。
	var big strings.Builder
	big.WriteString("diff --git a/a.go b/a.go\n@@ -1,1 +1,160 @@\n")
	for i := 0; i < 160; i++ {
		big.WriteString("+line\n")
	}
	in := toolSession("git diff", big.String())
	st := NewToolFilterStage(ToolFilterConfig{MinTokens: 1, RecencyGuard: 2, MinOutputChars: 16, HeadLines: 4, TailLines: 2})
	out, _, err := st.Apply(in)
	if err != nil {
		t.Fatal(err)
	}
	folded := singleTextOf(out[2])
	if !strings.Contains(folded, "a.go") {
		t.Errorf("folded output lost file name: %q", folded)
	}
	if !strings.Contains(folded, "lines truncated") {
		t.Errorf("folded output missing truncation marker: %q", folded)
	}
	if !strings.Contains(folded, "+160 -0") {
		t.Errorf("folded output missing full count: %q", folded)
	}
	if len(folded) >= len(big.String()) {
		t.Errorf("expected compaction, got %d >= %d", len(folded), big.Len())
	}
}
