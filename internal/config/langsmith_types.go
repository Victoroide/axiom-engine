package config

// LangSmith configures the optional LangSmith exporter.
type LangSmith struct {
	Enabled  bool
	Endpoint string
	APIKey   string `json:"-"`
	Project  string
}
