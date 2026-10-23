package embed

import "testing"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestFromEnvOffReturnsNil(t *testing.T) {
	if e := FromEnv(env(map[string]string{"MEMORYCARD_EMBED": "off"})); e != nil {
		t.Errorf("got %v, want nil", e)
	}
}

func TestFromEnvDefaultsAndOverrides(t *testing.T) {
	d := FromEnv(env(nil)).(*Ollama)
	if d.ModelName != DefaultModel || d.BaseURL != DefaultOllamaURL {
		t.Errorf("defaults = %q @ %q", d.ModelName, d.BaseURL)
	}
	o := FromEnv(env(map[string]string{"MEMORYCARD_EMBED_MODEL": "m", "MEMORYCARD_OLLAMA_URL": "http://h:1/"})).(*Ollama)
	if o.ModelName != "m" || o.BaseURL != "http://h:1" {
		t.Errorf("overrides = %q @ %q", o.ModelName, o.BaseURL)
	}
}
