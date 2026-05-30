package ai

// Config holds the runtime configuration for the AI subtitle engine. Values are
// loaded from server_settings (see internal/config) and mirror the existing
// recommendations embedding client: one OpenAI-compatible endpoint that the
// operator can point at OpenAI, Groq, a local Ollama/llama.cpp server, etc.
type Config struct {
	Enabled           bool
	BaseURL           string // e.g. "https://api.openai.com" (no trailing /v1)
	APIKey            string // empty for keyless local servers
	ChatModel         string // chat-completions model used for translation
	MaxConcurrentJobs int    // semaphore bound so jobs never starve transcodes
	BatchSize         int    // cues per translation request
	ContextNeighbors  int    // preceding source cues sent as untranslated context
}

// Ready reports whether the engine is enabled and minimally configured.
func (c Config) Ready() bool {
	return c.Enabled && c.BaseURL != "" && c.ChatModel != ""
}
