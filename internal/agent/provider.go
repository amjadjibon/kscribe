package agent

import "strings"

// Known OpenAI-compatible endpoint bases.
const (
	// GeminiBaseURL is Google Gemini's OpenAI-compatible endpoint base.
	GeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
	// ZAIBaseURL is Z.AI (Zhipu GLM) OpenAI-compatible endpoint base.
	ZAIBaseURL = "https://api.z.ai/api/paas/v4"
	// GroqBaseURL is Groq's OpenAI-compatible endpoint base.
	GroqBaseURL = "https://api.groq.com/openai/v1"
)

// ResolveBaseURL returns the effective API base for a provider. An explicit
// baseURL always wins; otherwise known providers map to their endpoint and
// everything else falls through to the OpenAI default (empty).
// these all speak the OpenAI chat-completions API, so no separate client.
func ResolveBaseURL(provider, baseURL string) string {
	if baseURL != "" {
		return baseURL
	}
	switch strings.ToLower(provider) {
	case "google", "gemini":
		return GeminiBaseURL
	case "zai", "z.ai", "zhipu", "glm":
		return ZAIBaseURL
	case "groq":
		return GroqBaseURL
	default:
		return ""
	}
}
