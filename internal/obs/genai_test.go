// genai_test.go 锁死 GenAI 关联字段契约：semconv 命名稳定、空字段
// 跳过、nil 安全、零值零字段（载荷永不进关联面由构造侧保证——
// 本包字段集只有 ID/名称/枚举）。
package obs

import (
	"strings"
	"testing"
)

func TestGenAICorrelationFieldNames(t *testing.T) {
	c := GenAICorrelation{
		Operation: OpChatCompletions, System: "zhipu",
		RequestModel: "@quality", ResponseModel: "glm-4.7-flash",
		RequestID: "req-1", ConversationID: "sess-1", TaskID: "t_1",
		ToolName: "omnifusion_task_get", ToolCallID: "call_1",
	}
	got := c.Fields()
	want := []string{
		FieldOperation, OpChatCompletions,
		FieldSystem, "zhipu",
		FieldRequestModel, "@quality",
		FieldResponseModel, "glm-4.7-flash",
		FieldRequestID, "req-1",
		FieldConversationID, "sess-1",
		FieldTaskID, "t_1",
		FieldToolName, "omnifusion_task_get",
		FieldToolCallID, "call_1",
	}
	if len(got) != len(want) {
		t.Fatalf("Fields() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Fields()[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestGenAICorrelationSkipsEmpty(t *testing.T) {
	c := GenAICorrelation{Operation: OpA2ATaskGet, TaskID: "t_9"}
	f := c.Fields()
	if len(f) != 4 {
		t.Fatalf("empty fields must be skipped, got %v", f)
	}
	var zero GenAICorrelation
	if got := zero.Fields(); len(got) != 0 {
		t.Fatalf("zero value must yield no fields, got %v", got)
	}
	var nilc *GenAICorrelation
	if nilc.Fields() != nil {
		t.Fatal("nil receiver must be safe")
	}
}

func TestGenAIFieldNamePrefix(t *testing.T) {
	// 契约：全部字段名以 gen_ai. 前缀开头（semconv 域）。
	for _, n := range []string{FieldRequestID, FieldOperation, FieldSystem,
		FieldRequestModel, FieldResponseModel, FieldConversationID,
		FieldTaskID, FieldToolName, FieldToolCallID} {
		if !strings.HasPrefix(n, "gen_ai.") {
			t.Fatalf("field %q lacks gen_ai. prefix", n)
		}
	}
}
