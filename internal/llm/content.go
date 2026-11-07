package llm

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	runtime "github.com/boundaryml/baml/engine/language_client_go/pkg"
	"go.opentelemetry.io/otel/trace"

	"github.com/Victoroide/axiom-engine/internal/telemetry"
)

type promptMessage struct{ role, content string }

func captureCollector(ctx context.Context, span trace.Span, collector runtime.Collector, provider string) {
	if !telemetry.CaptureContent(ctx) {
		return
	}
	log, err := collector.Last()
	if err != nil || log == nil {
		return
	}
	call, err := log.SelectedCall()
	if err == nil && call != nil {
		request, err := call.HttpRequest()
		if err == nil && request != nil {
			body, err := request.Body()
			if err == nil && body != nil {
				text, err := body.Text()
				if err == nil {
					for i, message := range promptMessages(provider, text) {
						prefix := "gen_ai.prompt." + strconv.Itoa(i)
						telemetry.SetContent(span, prefix+".role", message.role)
						telemetry.SetContent(span, prefix+".content", message.content)
					}
				}
			}
		}
	}
	if completion, err := log.RawLLMResponse(); err == nil && completion != "" {
		telemetry.SetContent(span, "gen_ai.completion.0.role", "assistant")
		telemetry.SetContent(span, "gen_ai.completion.0.content", completion)
	}
}

func promptMessages(provider, body string) []promptMessage {
	var request map[string]any
	if json.Unmarshal([]byte(body), &request) != nil {
		return []promptMessage{{"user", body}}
	}
	messages := make([]promptMessage, 0)
	appendMessage := func(role string, value any) {
		if role == "" {
			role = "user"
		}
		if role == "model" {
			role = "assistant"
		}
		if text := promptText(value); text != "" {
			messages = append(messages, promptMessage{role, text})
		}
	}
	switch provider {
	case "aws-bedrock", "anthropic":
		appendMessage("system", request["system"])
		for _, value := range messageList(request["messages"]) {
			appendMessage(messageRole(value), value["content"])
		}
	case "openai", "ollama", "openai-generic":
		for _, value := range messageList(request["messages"]) {
			appendMessage(messageRole(value), value["content"])
		}
	case "google-ai":
		appendMessage("system", request["systemInstruction"])
		for _, value := range messageList(request["contents"]) {
			appendMessage(messageRole(value), value["parts"])
		}
	default:
		return []promptMessage{{"user", body}}
	}
	return messages
}

func messageRole(value map[string]any) string { role, _ := value["role"].(string); return role }

func messageList(value any) []map[string]any {
	values, _ := value.([]any)
	messages := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if message, ok := value.(map[string]any); ok {
			messages = append(messages, message)
		}
	}
	return messages
}

func promptText(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case map[string]any:
		if text, ok := value["text"].(string); ok {
			return text
		}
		return promptText(value["parts"])
	case []any:
		parts := make([]string, 0, len(value))
		for _, part := range value {
			if text := promptText(part); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}
