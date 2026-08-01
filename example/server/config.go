package server

const configKey = "api.chat"

// Provider mirrors the CLI's config structure for LLM provider registration.
type Provider struct {
	Name    string
	Type    string
	BaseUrl string
	ApiKey  string
	Model   string
	Default bool
}
