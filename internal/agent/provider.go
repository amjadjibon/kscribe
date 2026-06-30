package agent

import "strings"

// GeminiBaseURL is Google Gemini's OpenAI-compatible endpoint base.
const GeminiBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"

// ResolveBaseURL returns the effective API base for a provider. An explicit
// baseURL always wins; otherwise google/gemini default to the Gemini endpoint
// and everything else falls through to the OpenAI default (empty).
// ponytail: Gemini speaks the OpenAI chat-completions API, so no separate client.
func ResolveBaseURL(provider, baseURL string) string {
	if baseURL != "" {
		return baseURL
	}
	switch strings.ToLower(provider) {
	case "google", "gemini":
		return GeminiBaseURL
	default:
		return ""
	}
}
