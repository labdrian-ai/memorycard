package embed

// DefaultModel is the embedding model memorycard is measured with.
const DefaultModel = "bge-m3"

// FromEnv builds the embedder described by the environment, or nil when
// MEMORYCARD_EMBED is "off". MEMORYCARD_OLLAMA_URL and MEMORYCARD_EMBED_MODEL
// override the defaults.
func FromEnv(getenv func(string) string) Embedder {
	if getenv("MEMORYCARD_EMBED") == "off" {
		return nil
	}
	model := getenv("MEMORYCARD_EMBED_MODEL")
	if model == "" {
		model = DefaultModel
	}
	return NewOllama(getenv("MEMORYCARD_OLLAMA_URL"), model)
}
