package routing

import (
	"context"
	"strings"
	"testing"
)

// TestQualityAutoNoDataClearError 无能力数据源时裸 @quality 必须返回可
// 行动的报错（指出改选具体模型），而不是"no attempts recorded"天书
//（v0.1.3 实测对话页默认模式撞上的降级路径）。
func TestQualityAutoNoDataClearError(t *testing.T) {
	r := newScoringRouter(t, "a", "b") // 无 Capability 装配
	_, _, err := r.Dispatch(context.Background(),
		testRequest(), WithQualityAuto())
	if err == nil {
		t.Fatal("dispatch should fail without capability data")
	}
	if !strings.Contains(err.Error(), "capability") || !strings.Contains(err.Error(), "pick a specific model") {
		t.Fatalf("error not actionable: %v", err)
	}
}
