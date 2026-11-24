package config

import (
	"fmt"
	"math"
	"strconv"
)

// PromptScaffoldingBytes reserves input bytes for instructions, the output
// schema and provider framing before any curriculum fields are counted.
const PromptScaffoldingBytes = 3072

// PlannerOutputTokens is the completion allowance requested from the provider.
const PlannerOutputTokens = 2048

const (
	// A valid graph can supply one is_a edge, a one-byte target with no
	// prerequisites, and the shortest learning style.
	minTopicInputBytes  = PromptScaffoldingBytes + len(`B -[is_a]-> A\n`) + len("Avisual")
	ollamaContextTokens = 4096
	// Mistral v0.3 uses 614 tokens for the fixed spans, including chat framing.
	// Reserve additional space for token boundaries around curriculum fields.
	ollamaScaffoldingTokens = 640
)

// PromptBudget bounds the curriculum sent with a study plan request.
type PromptBudget struct {
	MaxTopicBytes    int
	MaxRequestBytes  int
	MaxEdges         int
	MaxPrerequisites int
}

// DefaultPromptBudget returns the general curriculum limits before provider limits.
func DefaultPromptBudget() PromptBudget {
	return PromptBudget{MaxTopicBytes: 16_384, MaxRequestBytes: 131_072, MaxEdges: 256, MaxPrerequisites: 12}
}

func loadPromptBudget(getenv func(string) string, provider string) (PromptBudget, error) {
	budget := DefaultPromptBudget()
	var ollamaCeiling int
	if provider == "ollama" {
		var err error
		ollamaCeiling, err = ollamaTopicByteCeiling(getenv("LLM_OLLAMA_CONTEXT_TOKENS"))
		if err != nil {
			return PromptBudget{}, err
		}
		budget.MaxTopicBytes = min(budget.MaxTopicBytes, ollamaCeiling)
	}
	for _, item := range []struct {
		key string
		dst *int
	}{
		{"LLM_MAX_TOPIC_INPUT_BYTES", &budget.MaxTopicBytes},
		{"LLM_MAX_REQUEST_INPUT_BYTES", &budget.MaxRequestBytes},
		{"LLM_MAX_CONTEXT_EDGES", &budget.MaxEdges},
		{"LLM_MAX_PREREQUISITES", &budget.MaxPrerequisites},
	} {
		if value := getenv(item.key); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				return PromptBudget{}, fmt.Errorf("%s must be a positive integer", item.key)
			}
			*item.dst = n
		}
	}
	if budget.MaxTopicBytes < minTopicInputBytes {
		return PromptBudget{}, fmt.Errorf("LLM_MAX_TOPIC_INPUT_BYTES must be at least %d bytes", minTopicInputBytes)
	}
	if ollamaCeiling != 0 && budget.MaxTopicBytes > ollamaCeiling {
		return PromptBudget{}, fmt.Errorf("LLM_MAX_TOPIC_INPUT_BYTES must be at most %d bytes for the configured Ollama context", ollamaCeiling)
	}
	if budget.MaxRequestBytes < budget.MaxTopicBytes {
		return PromptBudget{}, fmt.Errorf("LLM_MAX_REQUEST_INPUT_BYTES must be at least LLM_MAX_TOPIC_INPUT_BYTES")
	}
	return budget, nil
}

func ollamaTopicByteCeiling(value string) (int, error) {
	const reserved = ollamaScaffoldingTokens + PlannerOutputTokens
	const minimum = reserved + minTopicInputBytes - PromptScaffoldingBytes
	contextTokens := ollamaContextTokens
	if value != "" {
		n, err := strconv.Atoi(value)
		if err != nil {
			return 0, fmt.Errorf("LLM_OLLAMA_CONTEXT_TOKENS must be an integer")
		}
		contextTokens = n
	}
	if contextTokens < minimum {
		return 0, fmt.Errorf("LLM_OLLAMA_CONTEXT_TOKENS must be at least %d tokens", minimum)
	}
	// For this profile, reserve up to one token per accounted curriculum byte.
	curriculumBytes := min(contextTokens-reserved, math.MaxInt-PromptScaffoldingBytes)
	return PromptScaffoldingBytes + curriculumBytes, nil
}
