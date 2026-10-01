package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func valid() Config {
	c := Defaults()
	c.AIEndpoint = "https://provider.example/v1/chat/completions"
	c.Model = "test"
	return c
}
func TestValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{"no model", func(c *Config) { c.Model = "" }}, {"bad port", func(c *Config) { c.Listen = "127.0.0.1:no" }}, {"public admin", func(c *Config) { c.AdminListen = "0.0.0.0:9090" }},
		{"no allowlist", func(c *Config) { c.AllowedCIDRs = nil }}, {"bad CIDR", func(c *Config) { c.AllowedCIDRs = []string{"bad"} }}, {"bad zone", func(c *Config) { c.Zone = "bad_thing" }},
		{"http remote", func(c *Config) { c.AIEndpoint = "http://provider.example/chat" }}, {"userinfo", func(c *Config) { c.AIEndpoint = "https://secret@provider.example/chat" }},
		{"key injection", func(c *Config) { c.APIKey = "secret\r\nHeader: bad" }}, {"token field", func(c *Config) { c.TokenField = "anything" }}, {"no budget", func(c *Config) { c.BudgetFile = "" }},
		{"no cache", func(c *Config) { c.CacheEntries = 0 }}, {"negative rate", func(c *Config) { c.AIRate.PerSecond = -1 }}, {"oversized answer", func(c *Config) { c.MaxAnswerBytes = 65535 }},
		{"short request", func(c *Config) { c.RequestTimeout = Duration(time.Second) }}, {"short drain", func(c *Config) { c.ShutdownTimeout = Duration(time.Second) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := valid()
			tt.change(&c)
			if c.Validate() == nil {
				t.Fatal("expected error")
			}
		})
	}
	c := valid()
	c.Zone = "Ask.Example.COM"
	if e := c.Validate(); e != nil || c.Zone != "ask.example.com." {
		t.Fatalf("normalization: %v %s", e, c.Zone)
	}
	c.AIEndpoint = "http://127.0.0.1:1234/chat"
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
}
func TestLoad(t *testing.T) {
	for _, k := range []string{"ASK53_AI_ENDPOINT", "ASK53_MODEL", "ASK53_API_KEY", "ASK53_API_KEY_FILE", "ASK53_LISTEN", "ASK53_ADMIN_LISTEN"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("ASK53_AI_ENDPOINT", "https://example.com/v1/chat/completions")
	t.Setenv("ASK53_MODEL", "mock")
	t.Setenv("ASK53_LISTEN", "127.0.0.1:5353")
	p := filepath.Join(t.TempDir(), "config.json")
	for _, s := range []string{`{"typo":1}`, `{} {}`, `{"ai_timeout":"nonsense"}`, `{"daily_calls":0}`} {
		os.WriteFile(p, []byte(s), 0600)
		if _, e := Load(p); e == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	key := filepath.Join(t.TempDir(), "key")
	os.WriteFile(key, []byte("file-secret\n"), 0600)
	os.WriteFile(p, []byte(`{"api_key_file":"`+key+`","cache_ttl":"2m"}`), 0600)
	c, e := Load(p)
	if e != nil || c.APIKey != "file-secret" || c.CacheTTL.Value() != 2*time.Minute {
		t.Fatalf("%+v %v", c, e)
	}
	t.Setenv("ASK53_API_KEY", "env-secret")
	c, e = Load(p)
	if e != nil || c.APIKey != "env-secret" {
		t.Fatal("env override failed", e)
	}
}
func TestNames(t *testing.T) {
	for _, s := range []string{".", "a..b.", "a_b.", "a\\046b.", "ایران.", strings.Repeat("a", 64) + ".", strings.Repeat("a.", 128)} {
		if ValidName(s) {
			t.Fatalf("accepted %q", s)
		}
	}
	if !ValidName(strings.Repeat("a", 63) + ".b.") {
		t.Fatal("valid label rejected")
	}
}
