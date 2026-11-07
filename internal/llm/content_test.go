package llm

import (
	"reflect"
	"testing"
)

func TestPromptMessages(t *testing.T) {
	for _, tc := range []struct {
		provider, body string
		want           []promptMessage
	}{
		{"aws-bedrock", `{"system":[{"text":"system"}],"messages":[{"role":"user","content":[{"text":"first"},{"text":"second"}]}]}`, []promptMessage{{"system", "system"}, {"user", "first\nsecond"}}},
		{"openai", `{"messages":[{"role":"system","content":"system"},{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"private"}}]}]}`, []promptMessage{{"system", "system"}, {"user", "hello"}}},
		{"ollama", `{"messages":[{"role":"user","content":"hello"}]}`, []promptMessage{{"user", "hello"}}},
		{"openai-generic", `{"messages":[{"role":"user","content":"hello"}]}`, []promptMessage{{"user", "hello"}}},
		{"anthropic", `{"system":"system","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`, []promptMessage{{"system", "system"}, {"user", "hello"}}},
		{"anthropic", `{"system":[{"type":"text","text":"system"}],"messages":[{"role":"user","content":"hello"}]}`, []promptMessage{{"system", "system"}, {"user", "hello"}}},
		{"google-ai", `{"systemInstruction":{"parts":[{"text":"system"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"reply"}]}]}`, []promptMessage{{"system", "system"}, {"user", "hello"}, {"assistant", "reply"}}},
		{"other", `{"text":"hello"}`, []promptMessage{{"user", `{"text":"hello"}`}}},
		{"openai", `raw text`, []promptMessage{{"user", "raw text"}}},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			if got := promptMessages(tc.provider, tc.body); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("messages=%v want=%v", got, tc.want)
			}
		})
	}
}
