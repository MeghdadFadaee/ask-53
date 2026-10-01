package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeghdadFadaee/ask-53/internal/config"
)

func clientFor(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c := config.Defaults()
	c.AIEndpoint = s.URL + "/v1/chat/completions"
	c.Model = "test"
	c.APIKey = "test-secret"
	client := New(c)
	t.Cleanup(client.Close)
	return client
}
func success(w http.ResponseWriter, text, reason string) {
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}, "finish_reason": reason}}})
}
func TestRequestAndSanitize(t *testing.T) {
	for _, field := range []string{"max_tokens", "max_completion_tokens"} {
		t.Run(field, func(t *testing.T) {
			client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-secret" {
					t.Error("wrong request")
				}
				var b map[string]any
				json.NewDecoder(r.Body).Decode(&b)
				if b[field] != float64(128) || b["model"] != "test" || b["n"] != float64(1) || b["stream"] != false {
					t.Errorf("body: %v", b)
				}
				msgs := b["messages"].([]any)
				if msgs[1].(map[string]any)["content"] != "capital of iran" {
					t.Error("question missing")
				}
				success(w, "\t Tehran\n\x1b\u202e", "stop")
			})
			client.cfg.TokenField = field
			s, e := client.Answer(context.Background(), "capital of iran")
			if e != nil || s != "Tehran" {
				t.Fatalf("%q %v", s, e)
			}
		})
	}
}
func TestProviderFailures(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		kind    string
	}{
		{"auth", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "secret internal text", 401) }, "http_401"},
		{"rate", func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Retry-After", "20"); w.WriteHeader(429) }, "http_429"},
		{"server", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }, "http_503"},
		{"json", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "not json") }, "invalid_response"},
		{"empty choices", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"choices":[]}`) }, "invalid_response"},
		{"null", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"choices":[{"message":{"content":null},"finish_reason":"stop"}]}`)
		}, "invalid_response"},
		{"empty answer", func(w http.ResponseWriter, r *http.Request) { success(w, "\n", "stop") }, "empty_answer"},
		{"length", func(w http.ResponseWriter, r *http.Request) { success(w, "partial", "length") }, "incomplete_answer"},
		{"filter", func(w http.ResponseWriter, r *http.Request) { success(w, "", "content_filter") }, "incomplete_answer"},
		{"tools", func(w http.ResponseWriter, r *http.Request) { success(w, "something", "tool_calls") }, "incomplete_answer"},
		{"overlong", func(w http.ResponseWriter, r *http.Request) { success(w, strings.Repeat("a", 257), "stop") }, "answer_limit"},
		{"body limit", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("a", 65537)) }, "body_limit"},
		{"bad utf8", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte{0xff}) }, "invalid_text"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			client := clientFor(t, tt.handler)
			_, e := client.Answer(context.Background(), "test")
			pe, ok := e.(*Error)
			if !ok || pe.Kind != tt.kind {
				t.Fatalf("got %v", e)
			}
			if strings.Contains(e.Error(), "secret") {
				t.Fatal("leaked provider body")
			}
			if tt.name == "rate" && pe.RetryAfter != 20*time.Second {
				t.Fatal("retry-after lost")
			}
		})
	}
}
func TestTimeoutAndCancellation(t *testing.T) {
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, e := client.Answer(ctx, "test")
	if e == nil || e.(*Error).Kind != "timeout" {
		t.Fatal(e)
	}
}
func TestRedirectNotFollowed(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	client := clientFor(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) })
	_, e := client.Answer(context.Background(), "test")
	if e == nil || hits.Load() != 0 {
		t.Fatal("followed credential redirect", e)
	}
}
func TestRetryAfter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tt := range []struct {
		s    string
		want time.Duration
	}{{"-2", 0}, {"garbage", 0}, {"7200", time.Hour}, {now.Add(7 * time.Second).Format(http.TimeFormat), 7 * time.Second}} {
		if d := retryAfter(tt.s, now); d != tt.want {
			t.Errorf("%s: %s", tt.s, d)
		}
	}
}
