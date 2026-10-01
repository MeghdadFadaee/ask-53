// Package config loads validated configuration. Unknown JSON fields are errors.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	*d = Duration(v)
	return err
}
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }
func (d Duration) Value() time.Duration         { return time.Duration(d) }

type Rate struct {
	PerSecond float64 `json:"per_second"`
	Burst     int     `json:"burst"`
}
type Config struct {
	Listen            string   `json:"listen"`
	AdminListen       string   `json:"admin_listen"`
	Zone              string   `json:"zone"`
	AllowedCIDRs      []string `json:"allowed_cidrs"`
	UDPMode           string   `json:"udp_mode"`
	AIEndpoint        string   `json:"ai_endpoint"`
	Model             string   `json:"model"`
	APIKeyFile        string   `json:"api_key_file"`
	APIKey            string   `json:"-"`
	AllowInsecureAI   bool     `json:"allow_insecure_ai"`
	TokenField        string   `json:"token_field"`
	MaxTokens         int      `json:"max_tokens"`
	ReasoningEffort   string   `json:"reasoning_effort"`
	AITimeout         Duration `json:"ai_timeout"`
	RequestTimeout    Duration `json:"request_timeout"`
	ShutdownTimeout   Duration `json:"shutdown_timeout"`
	CacheTTL          Duration `json:"cache_ttl"`
	FailureTTL        Duration `json:"failure_ttl"`
	CacheEntries      int      `json:"cache_entries"`
	MaxAnswerBytes    int      `json:"max_answer_bytes"`
	MaxInflight       int      `json:"max_inflight"`
	MaxHandlers       int      `json:"max_handlers"`
	MaxTCPConnections int      `json:"max_tcp_connections"`
	ClientEntries     int      `json:"client_entries"`
	ClientIdleTTL     Duration `json:"client_idle_ttl"`
	IngressRate       Rate     `json:"ingress_rate"`
	ClientRate        Rate     `json:"client_rate"`
	AIRate            Rate     `json:"ai_rate"`
	ClientAIRate      Rate     `json:"client_ai_rate"`
	DailyCalls        int      `json:"daily_calls"`
	BudgetFile        string   `json:"budget_file"`
	CircuitFailures   int      `json:"circuit_failures"`
	CircuitCooldown   Duration `json:"circuit_cooldown"`
	TCPIdleTimeout    Duration `json:"tcp_idle_timeout"`
}

func Defaults() Config {
	return Config{
		Listen: "127.0.0.1:5353", AdminListen: "127.0.0.1:9090", AllowedCIDRs: []string{"127.0.0.0/8", "::1/128"}, UDPMode: "loopback",
		TokenField: "max_completion_tokens", MaxTokens: 128, AITimeout: Duration(8 * time.Second), RequestTimeout: Duration(9 * time.Second), ShutdownTimeout: Duration(12 * time.Second),
		CacheTTL: Duration(time.Hour), FailureTTL: Duration(5 * time.Second), CacheEntries: 4096, MaxAnswerBytes: 256, MaxInflight: 8, MaxHandlers: 128, MaxTCPConnections: 128,
		ClientEntries: 4096, ClientIdleTTL: Duration(10 * time.Minute), IngressRate: Rate{200, 400}, ClientRate: Rate{10, 20}, AIRate: Rate{1, 4}, ClientAIRate: Rate{0.1, 2},
		DailyCalls: 100, BudgetFile: "var/budget.json", CircuitFailures: 3, CircuitCooldown: Duration(30 * time.Second), TCPIdleTimeout: Duration(5 * time.Second),
	}
}
func Load(path string) (Config, error) {
	c := Defaults()
	if path != "" {
		f, e := os.Open(path)
		if e != nil {
			return c, e
		}
		defer f.Close()
		b, e := io.ReadAll(io.LimitReader(f, 65537))
		if e != nil {
			return c, e
		}
		b = bytes.TrimSpace(b)
		if len(b) == 0 || len(b) > 65536 || b[0] != '{' {
			return c, errors.New("config must be a JSON object <= 65536 bytes")
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if e = dec.Decode(&c); e != nil {
			return c, fmt.Errorf("config: %w", e)
		}
		if e = dec.Decode(new(any)); e != io.EOF {
			return c, errors.New("config must contain one JSON object")
		}
	}
	for _, v := range []struct {
		key string
		dst *string
	}{{"ASK53_LISTEN", &c.Listen}, {"ASK53_ADMIN_LISTEN", &c.AdminListen}, {"ASK53_AI_ENDPOINT", &c.AIEndpoint}, {"ASK53_MODEL", &c.Model}, {"ASK53_API_KEY_FILE", &c.APIKeyFile}} {
		if s, ok := os.LookupEnv(v.key); ok {
			*v.dst = s
		}
	}
	c.APIKey = os.Getenv("ASK53_API_KEY")
	if c.APIKey == "" && c.APIKeyFile != "" {
		f, e := os.Open(c.APIKeyFile)
		if e != nil {
			return c, fmt.Errorf("read API key file: %w", e)
		}
		b, e := io.ReadAll(io.LimitReader(f, 8193))
		f.Close()
		if len(b) > 8192 {
			return c, errors.New("API key file exceeds 8192 bytes")
		}
		if e != nil {
			return c, fmt.Errorf("read API key file: %w", e)
		}
		c.APIKey = strings.TrimSpace(string(b))
	}
	return c, c.Validate()
}
func (c *Config) Validate() error {
	host, port, e := net.SplitHostPort(c.Listen)
	if e != nil {
		return fmt.Errorf("listen: %w", e)
	}
	if host != "" {
		if _, e = netip.ParseAddr(host); e != nil {
			return errors.New("listen host must be a literal IP")
		}
	}
	p, e := strconv.Atoi(port)
	if e != nil || p < 0 || p > 65535 {
		return errors.New("invalid listen port")
	}
	if c.AdminListen != "" {
		h, p, e := net.SplitHostPort(c.AdminListen)
		a, ae := netip.ParseAddr(h)
		n, ne := strconv.Atoi(p)
		if e != nil || ae != nil || !a.IsLoopback() || ne != nil || n < 0 || n > 65535 {
			return errors.New("admin_listen must be a loopback IP and valid port")
		}
	}
	if len(c.AllowedCIDRs) == 0 || len(c.AllowedCIDRs) > 128 {
		return errors.New("allowed_cidrs must contain 1..128 networks")
	}
	for _, s := range c.AllowedCIDRs {
		if _, e = netip.ParsePrefix(s); e != nil {
			return fmt.Errorf("allowed_cidrs: %w", e)
		}
	}
	if c.UDPMode != "loopback" && c.UDPMode != "tcp-only" {
		return errors.New("udp_mode must be loopback or tcp-only")
	}
	if c.Zone != "" {
		c.Zone = strings.ToLower(strings.TrimSuffix(c.Zone, ".")) + "."
		if !ValidName(c.Zone) {
			return errors.New("invalid zone")
		}
	}
	u, e := url.Parse(c.AIEndpoint)
	if e != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("ai_endpoint must be an absolute HTTP(S) completion URL without credentials/query/fragment")
	}
	a, _ := netip.ParseAddr(u.Hostname())
	if u.Scheme == "http" && !a.IsLoopback() && !c.AllowInsecureAI {
		return errors.New("non-loopback AI requires HTTPS or explicit allow_insecure_ai")
	}
	if strings.TrimSpace(c.Model) == "" || len(c.Model) > 256 {
		return errors.New("model is required (max 256 bytes)")
	}
	if len(c.APIKey) > 8192 || strings.ContainsAny(c.APIKey, "\r\n") {
		return errors.New("invalid API key")
	}
	if c.TokenField != "max_tokens" && c.TokenField != "max_completion_tokens" {
		return errors.New("invalid token_field")
	}
	if c.ReasoningEffort != "" {
		switch c.ReasoningEffort {
		case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		default:
			return errors.New("invalid reasoning_effort")
		}
	}
	for _, v := range []struct {
		n           string
		v, min, max int
	}{
		{"max_tokens", c.MaxTokens, 1, 4096}, {"cache_entries", c.CacheEntries, 1, 100000}, {"max_answer_bytes", c.MaxAnswerBytes, 1, 2048}, {"max_inflight", c.MaxInflight, 1, 256}, {"max_handlers", c.MaxHandlers, 1, 4096}, {"max_tcp_connections", c.MaxTCPConnections, 1, 4096}, {"client_entries", c.ClientEntries, 1, 100000}, {"daily_calls", c.DailyCalls, 1, 1000000}, {"circuit_failures", c.CircuitFailures, 1, 100},
	} {
		if v.v < v.min || v.v > v.max {
			return fmt.Errorf("%s must be %d..%d", v.n, v.min, v.max)
		}
	}
	if c.MaxInflight > c.MaxHandlers {
		return errors.New("max_inflight must not exceed max_handlers")
	}
	if c.BudgetFile == "" {
		return errors.New("budget_file is required")
	}
	for _, v := range []struct {
		n   string
		v   Duration
		max time.Duration
	}{{"ai_timeout", c.AITimeout, time.Minute}, {"request_timeout", c.RequestTimeout, 2 * time.Minute}, {"shutdown_timeout", c.ShutdownTimeout, 3 * time.Minute}, {"cache_ttl", c.CacheTTL, 24 * time.Hour}, {"failure_ttl", c.FailureTTL, time.Minute}, {"client_idle_ttl", c.ClientIdleTTL, time.Hour}, {"circuit_cooldown", c.CircuitCooldown, time.Hour}, {"tcp_idle_timeout", c.TCPIdleTimeout, time.Minute}} {
		if v.v.Value() < time.Millisecond || v.v.Value() > v.max {
			return fmt.Errorf("invalid %s", v.n)
		}
	}
	if c.RequestTimeout < c.AITimeout || c.ShutdownTimeout < c.RequestTimeout {
		return errors.New("timeouts must satisfy ai_timeout <= request_timeout <= shutdown_timeout")
	}
	for _, r := range []Rate{c.IngressRate, c.ClientRate, c.AIRate, c.ClientAIRate} {
		if math.IsNaN(r.PerSecond) || math.IsInf(r.PerSecond, 0) || r.PerSecond <= 0 || r.PerSecond > 100000 || r.Burst < 1 || r.Burst > 100000 {
			return errors.New("rates must be positive and bounded")
		}
	}
	return nil
}

// We intentionally support a narrower interface than arbitrary binary DNS labels.
func ValidName(name string) bool {
	if !strings.HasSuffix(name, ".") || len(name) > 254 || name == "." {
		return false
	}
	for _, l := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if len(l) < 1 || len(l) > 63 {
			return false
		}
		for _, r := range l {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}
