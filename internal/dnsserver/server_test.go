package dnsserver

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"ask53/internal/ai"
	"ask53/internal/config"
	"ask53/internal/service"
	"github.com/miekg/dns"
)

type providerFunc func(context.Context, string) (string, error)

func (f providerFunc) Answer(ctx context.Context, q string) (string, error) { return f(ctx, q) }
func testConfig(t *testing.T) config.Config {
	t.Helper()
	c := config.Defaults()
	c.Listen = "127.0.0.1:0"
	c.AdminListen = ""
	c.BudgetFile = filepath.Join(t.TempDir(), "budget.json")
	c.AIEndpoint = "http://127.0.0.1:1/completions"
	c.Model = "test"
	c.IngressRate = config.Rate{PerSecond: 10000, Burst: 10000}
	c.ClientRate = config.Rate{PerSecond: 10000, Burst: 10000}
	c.AIRate = config.Rate{PerSecond: 10000, Burst: 10000}
	c.ClientAIRate = config.Rate{PerSecond: 10000, Burst: 10000}
	return c
}
func fixture(t *testing.T, c config.Config, p service.Provider) (*Server, *Handler, *service.Stats) {
	t.Helper()
	b, err := service.OpenBudget(c.BudgetFile, c.DailyCalls)
	if err != nil {
		t.Fatal(err)
	}
	g := service.NewGate(c)
	stats := new(service.Stats)
	engine := service.NewEngine(c, p, b, g, stats, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s, err := Start(c, engine, g, stats)
	if err != nil {
		engine.Close()
		b.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		s.Close(ctx)
		engine.Close()
		b.Close()
	})
	return s, NewHandler(c, engine, g, stats), stats
}
func exchange(t *testing.T, addr, transport, name string, typ uint16, edns int) *dns.Msg {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(name, typ)
	if edns >= 0 {
		q.SetEdns0(uint16(edns), false)
	}
	r, _, e := (&dns.Client{Net: transport, Timeout: 2 * time.Second}).Exchange(q, addr)
	if e != nil {
		t.Fatal(e)
	}
	if r.Id != q.Id || !r.Response || r.RecursionAvailable || r.AuthenticatedData {
		t.Fatalf("bad reply flags: %+v", r.MsgHdr)
	}
	return r
}
func TestNetworkCacheConcurrencyAndProtocol(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var b struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		if b.Messages[1].Content != "capital of iran" {
			t.Error("wrong prompt", b)
		}
		time.Sleep(30 * time.Millisecond)
		io.WriteString(w, `{"choices":[{"message":{"content":"Tehran"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	c := testConfig(t)
	c.AIEndpoint = upstream.URL + "/v1/chat/completions"
	client := ai.New(c)
	defer client.Close()
	s, _, stats := fixture(t, c, client)
	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			transport := "udp"
			name := "capital.of.iran."
			if i%2 == 0 {
				transport = "tcp"
				name = "Capital.Of.IRAN."
			}
			r := exchange(t, s.Addr(), transport, name, dns.TypeTXT, 1232)
			if r.Rcode != dns.RcodeSuccess || len(r.Answer) != 1 || r.Answer[0].(*dns.TXT).Txt[0] != "Tehran" {
				t.Error(r)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("duplicate upstream work", calls.Load())
	}
	r := exchange(t, s.Addr(), "udp", "capital.of.iran.", dns.TypeTXT, -1)
	if len(r.Answer) != 1 || stats.CacheHits.Load() < 1 || r.Answer[0].Header().Ttl > 3600 {
		t.Fatal("invalid cache response")
	}
	for _, typ := range []uint16{dns.TypeANY, dns.TypeA, dns.TypeAXFR, dns.TypeIXFR, dns.TypeSOA} {
		r = exchange(t, s.Addr(), "tcp", "capital.of.iran.", typ, 1232)
		if r.Rcode != dns.RcodeRefused || len(r.Answer) != 0 {
			t.Error("unexpected type accepted", typ)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("unsupported type spent money")
	}
}
func TestTCPFallbackWithDig(t *testing.T) {
	c := testConfig(t)
	c.UDPMode = "tcp-only"
	var calls atomic.Int64
	s, _, _ := fixture(t, c, providerFunc(func(context.Context, string) (string, error) { calls.Add(1); return "Tehran", nil }))
	r := exchange(t, s.Addr(), "udp", "capital.of.iran.", dns.TypeTXT, 4096)
	if !r.Truncated || len(r.Answer) != 0 || calls.Load() != 0 {
		t.Fatal("UDP triggered paid work")
	}
	if _, err := exec.LookPath("dig"); err != nil {
		t.Skip("dig unavailable; TCP/UDP covered separately")
	}
	host, port, _ := net.SplitHostPort(s.Addr())
	out, err := exec.Command("dig", "@"+host, "-p", port, "capital.of.iran.", "TXT", "+short", "+time=2", "+tries=1").CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"Tehran"`) || calls.Load() != 1 {
		t.Fatalf("dig fallback: %s %v calls=%d", out, err, calls.Load())
	}
}
func TestUDPSizeAndTXTChunks(t *testing.T) {
	for _, edns := range []int{-1, 100, 512, 1232, 4096} {
		t.Run(strings.ReplaceAll(time.Duration(edns).String(), "-", "n"), func(t *testing.T) {
			c := testConfig(t)
			c.MaxAnswerBytes = 1500
			text := strings.Repeat("آ", 250)
			s, _, _ := fixture(t, c, providerFunc(func(context.Context, string) (string, error) { return text, nil }))
			r := exchange(t, s.Addr(), "udp", "q.", dns.TypeTXT, edns)
			b, err := r.Pack()
			if err != nil {
				t.Fatal(err)
			}
			limit := 512
			if edns > 512 {
				limit = min(edns, 1232)
			}
			if len(b) > limit {
				t.Fatal("oversized UDP packet", len(b))
			}
			if edns <= 512 {
				if !r.Truncated || len(r.Answer) != 0 {
					t.Fatal("missing truncation")
				}
				r = exchange(t, s.Addr(), "tcp", "q.", dns.TypeTXT, -1)
			}
			b, err = r.Pack()
			if err != nil {
				t.Fatal(err)
			}
			got, err := wireTXT(b)
			if err != nil || got != text {
				t.Fatalf("TXT data corrupted: %q %v", got, err)
			}
			if len(r.Answer[0].(*dns.TXT).Txt) != 2 {
				t.Fatal("not split into strings")
			}
			if opt := r.IsEdns0(); opt != nil && opt.UDPSize() != 1232 {
				t.Fatal("unbounded advertised EDNS size")
			}
		})
	}
}
func TestTXTQuotesAndBackslashes(t *testing.T) {
	text := `path C:\new\123 "quoted"`
	c := testConfig(t)
	s, _, _ := fixture(t, c, providerFunc(func(context.Context, string) (string, error) { return text, nil }))
	r := exchange(t, s.Addr(), "tcp", "q.", dns.TypeTXT, -1)
	b, err := r.Pack()
	if err != nil {
		t.Fatal(err)
	}
	got, err := wireTXT(b)
	if err != nil || got != text {
		t.Fatalf("corruption %q != %q: %v", got, text, err)
	}
}

// Inspect actual wire character strings, independent of the library's escaped
// presentation text. Each individual chunk must remain valid UTF-8.
func wireTXT(b []byte) (string, error) {
	_, off, e := dns.UnpackDomainName(b, 12)
	if e != nil {
		return "", e
	}
	off += 4
	_, off, e = dns.UnpackDomainName(b, off)
	if e != nil || off+10 > len(b) {
		return "", errors.New("missing answer")
	}
	length := int(binary.BigEndian.Uint16(b[off+8 : off+10]))
	off += 10
	if off+length > len(b) {
		return "", errors.New("short RDATA")
	}
	end := off + length
	var text strings.Builder
	for off < end {
		n := int(b[off])
		off++
		if off+n > end || !utf8.Valid(b[off:off+n]) {
			return "", errors.New("invalid TXT chunk")
		}
		text.Write(b[off : off+n])
		off += n
	}
	return text.String(), nil
}
func TestNameBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name, zone, want string
		valid            bool
	}{{"Capital.Of.IRAN.", "", "capital of iran", true}, {"capital.of.iran.ask.example.com.", "ask.example.com.", "capital of iran", true}, {"ask.example.com.", "ask.example.com.", "", false}, {"capital.of.iran.evilask.example.com.", "ask.example.com.", "", false}, {"what.is-x.", "", "what is-x", true}, {"a_b.", "", "", false}, {"a\\046b.", "", "", false}, {".", "", "", false}} {
		got, err := Question(tt.name, tt.zone)
		if (err == nil) != tt.valid || got != tt.want {
			t.Errorf("%s: %q %v", tt.name, got, err)
		}
	}
	// 255 wire bytes: length bytes per label + terminating root byte.
	name := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61) + "."
	if _, err := Question(name, ""); err != nil {
		t.Fatal("max legal name rejected", err)
	}
	if _, err := Question(name+"a.", ""); err == nil {
		t.Fatal("overlong name accepted")
	}
}

type recorder struct {
	addr   net.Addr
	msg    *dns.Msg
	closed bool
}

func (r *recorder) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5353}
}
func (r *recorder) RemoteAddr() net.Addr        { return r.addr }
func (r *recorder) WriteMsg(m *dns.Msg) error   { r.msg = m.Copy(); return nil }
func (r *recorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *recorder) Close() error                { r.closed = true; return nil }
func (r *recorder) TsigStatus() error           { return nil }
func (r *recorder) TsigTimersOnly(bool)         {}
func (r *recorder) Hijack()                     {}
func TestPublicUDPAndAccessControl(t *testing.T) {
	c := testConfig(t)
	c.AllowedCIDRs = []string{"127.0.0.0/8", "198.51.100.0/24"}
	var calls atomic.Int64
	_, h, _ := fixture(t, c, providerFunc(func(context.Context, string) (string, error) { calls.Add(1); return "ok", nil }))
	q := new(dns.Msg)
	q.SetQuestion("q.", dns.TypeTXT)
	for _, ip := range []string{"198.51.100.1", "198.51.100.2"} {
		w := &recorder{addr: &net.UDPAddr{IP: net.ParseIP(ip), Port: 1234}}
		h.ServeDNS(w, q)
		if w.msg == nil || !w.msg.Truncated || len(w.msg.Answer) != 0 {
			t.Fatal("public UDP answered")
		}
		b, _ := w.msg.Pack()
		qb, _ := q.Pack()
		if len(b) > len(qb) {
			t.Fatal("public response amplification")
		}
	}
	w := &recorder{addr: &net.TCPAddr{IP: net.ParseIP("203.0.113.1"), Port: 1234}}
	h.ServeDNS(w, q)
	if !w.closed || w.msg != nil || calls.Load() != 0 {
		t.Fatal("allowlist not enforced")
	}
}
func TestEDNSAndMalformedRequests(t *testing.T) {
	c := testConfig(t)
	var calls atomic.Int64
	s, h, _ := fixture(t, c, providerFunc(func(context.Context, string) (string, error) { calls.Add(1); return "ok", nil }))
	cases := []struct {
		name   string
		mutate func(*dns.Msg)
		code   int
	}{
		{"no question", func(q *dns.Msg) { q.Question = nil }, dns.RcodeFormatError}, {"two questions", func(q *dns.Msg) { q.Question = append(q.Question, q.Question[0]) }, dns.RcodeFormatError},
		{"class", func(q *dns.Msg) { q.Question[0].Qclass = dns.ClassCHAOS }, dns.RcodeRefused}, {"truncated", func(q *dns.Msg) { q.Truncated = true }, dns.RcodeFormatError},
		{"zero", func(q *dns.Msg) { q.Zero = true }, dns.RcodeFormatError}, {"response code", func(q *dns.Msg) { q.Rcode = 2 }, dns.RcodeFormatError},
		{"notify", func(q *dns.Msg) { q.Opcode = dns.OpcodeNotify }, dns.RcodeNotImplemented}, {"update", func(q *dns.Msg) { q.Opcode = dns.OpcodeUpdate }, dns.RcodeNotImplemented},
		{"bad EDNS version", func(q *dns.Msg) { q.SetEdns0(4096, true); q.IsEdns0().SetVersion(1) }, dns.RcodeBadVers},
		{"bad OPT owner", func(q *dns.Msg) { q.SetEdns0(1232, false); q.IsEdns0().Hdr.Name = "evil." }, dns.RcodeFormatError},
		{"two OPTs", func(q *dns.Msg) { q.SetEdns0(1232, false); q.Extra = append(q.Extra, q.Extra[0]) }, dns.RcodeFormatError},
		{"arbitrary extra", func(q *dns.Msg) {
			q.Extra = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "q.", Rrtype: dns.TypeA, Class: 1}, A: net.ParseIP("127.0.0.1")}}
		}, dns.RcodeFormatError},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			q := new(dns.Msg)
			q.SetQuestion("q.", dns.TypeTXT)
			tt.mutate(q)
			w := &recorder{addr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}}
			h.ServeDNS(w, q)
			if w.msg == nil || w.msg.Rcode != tt.code {
				t.Fatalf("reply %v", w.msg)
			}
			b, err := w.msg.Pack()
			if err != nil {
				t.Fatal(err)
			}
			round := new(dns.Msg)
			if err = round.Unpack(b); err != nil || round.Rcode != tt.code {
				t.Fatal("rcode wire corruption", err, round)
			}
		})
	}
	// Actual malformed wire messages, including a compression-pointer cycle,
	// missing question bytes, illegal label lengths, and an unsolicited response.
	packets := [][]byte{{0, 1}, make([]byte, 12), {0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xc0, 0x0c, 0, 16, 0, 1}, {0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 64, 'a', 0, 0, 16, 0, 1}, {0, 1, 128, 0, 0, 0, 0, 0, 0, 0, 0, 0}}
	for _, b := range packets {
		conn, err := net.Dial("udp", s.Addr())
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(40 * time.Millisecond))
		conn.Write(b)
		buf := make([]byte, 512)
		n, _ := conn.Read(buf)
		conn.Close()
		if n > len(b) && len(b) >= 12 {
			t.Fatal("malformed reply amplification")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("malformed query called provider")
	}
	r := exchange(t, s.Addr(), "tcp", "valid.", dns.TypeTXT, -1)
	if r.Rcode != 0 {
		t.Fatal("server stopped after malformed packets")
	}
}
func TestShutdownRestartAndIdleTCP(t *testing.T) {
	c := testConfig(t)
	s, _, _ := fixture(t, c, providerFunc(func(context.Context, string) (string, error) { return "ok", nil }))
	idle, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	addr := s.Addr()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	c.Listen = addr
	c.BudgetFile = filepath.Join(t.TempDir(), "budget.json")
	next, _, _ := fixture(t, c, providerFunc(func(context.Context, string) (string, error) { return "restarted", nil }))
	r := exchange(t, next.Addr(), "tcp", "q.", dns.TypeTXT, -1)
	if r.Answer[0].(*dns.TXT).Txt[0] != "restarted" {
		t.Fatal("restart failed")
	}
}
func TestProviderFailureDNSCode(t *testing.T) {
	c := testConfig(t)
	var calls atomic.Int64
	s, _, _ := fixture(t, c, providerFunc(func(context.Context, string) (string, error) { calls.Add(1); return "", &ai.Error{Kind: "http_503"} }))
	for range 2 {
		r := exchange(t, s.Addr(), "tcp", "q.", dns.TypeTXT, -1)
		if r.Rcode != dns.RcodeServerFailure || len(r.Answer) > 0 {
			t.Fatal("provider error returned as successful TXT")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("failure cache missing")
	}
}
func FuzzQuestion(f *testing.F) {
	for _, s := range []string{"capital.of.iran.", "a\\046b.", ".", "a..b.", strings.Repeat("a", 63) + "."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		q, e := Question(name, "")
		if e == nil && (q == "" || len(q) > 253) {
			t.Fatal("invalid normalized question")
		}
	})
}
func FuzzTXTWire(f *testing.F) {
	for _, s := range []string{`x\123"y`, strings.Repeat("آ", 260), strings.Repeat("x", 255), "Tehran"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if text == "" || len(text) > 2048 || !utf8.ValidString(text) {
			t.Skip()
		}
		m := new(dns.Msg)
		m.SetQuestion("q.", dns.TypeTXT)
		m.Response = true
		m.Answer = []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: "q.", Rrtype: dns.TypeTXT, Class: 1}, Txt: txtStrings(text)}}
		b, e := m.Pack()
		if e != nil {
			t.Fatal(e)
		}
		got, e := wireTXT(b)
		if e != nil || got != text {
			t.Fatal("wire corruption", e)
		}
	})
}

func TestGracefulDrainActiveQuery(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	c := testConfig(t)
	s, _, _ := fixture(t, c, providerFunc(func(ctx context.Context, q string) (string, error) {
		close(started)
		select {
		case <-release:
			return "drained", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}))
	reply := make(chan *dns.Msg)
	go func() { reply <- exchange(t, s.Addr(), "tcp", "q.", dns.TypeTXT, -1) }()
	<-started
	closed := make(chan error)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { closed <- s.Close(ctx) }()
	select {
	case <-closed:
		t.Fatal("shutdown did not drain active request")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if r := <-reply; r.Rcode != 0 || r.Answer[0].(*dns.TXT).Txt[0] != "drained" {
		t.Fatal("answer lost")
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}
func TestTCPConnectionsAndRequestBounds(t *testing.T) {
	c := testConfig(t)
	c.MaxTCPConnections = 1
	c.TCPIdleTimeout = config.Duration(50 * time.Millisecond)
	s, _, stats := fixture(t, c, providerFunc(func(context.Context, string) (string, error) { return "ok", nil }))
	one, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	one.SetDeadline(time.Now().Add(time.Second))
	q := new(dns.Msg)
	q.SetQuestion("q.", dns.TypeTXT)
	b, _ := q.Pack()
	frame := make([]byte, 2+len(b))
	binary.BigEndian.PutUint16(frame, uint16(len(b)))
	copy(frame[2:], b)
	one.Write(frame)
	var hdr [2]byte
	if _, err = io.ReadFull(one, hdr[:]); err != nil {
		t.Fatal(err)
	}
	io.ReadFull(one, make([]byte, int(binary.BigEndian.Uint16(hdr[:]))))
	two, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	two.SetDeadline(time.Now().Add(time.Second))
	two.Write(frame)
	_, err = two.Read(make([]byte, 2))
	two.Close()
	if err == nil || stats.Limited.Load() == 0 {
		t.Fatal("TCP cap not enforced")
	}
	time.Sleep(80 * time.Millisecond)
	one.Close()
	r := exchange(t, s.Addr(), "tcp", "q.", dns.TypeTXT, -1)
	if r.Rcode != 0 {
		t.Fatal("TCP slot not released")
	}
	over, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	over.SetDeadline(time.Now().Add(time.Second))
	bad := make([]byte, maxUDP+2)
	binary.BigEndian.PutUint16(bad, maxUDP)
	over.Write(bad)
	_, err = over.Read(make([]byte, 2))
	over.Close()
	if err == nil {
		t.Fatal("oversized request accepted")
	}
}
func TestWaiterCapacity(t *testing.T) {
	c := testConfig(t)
	c.MaxHandlers = 1
	c.MaxInflight = 1
	started := make(chan struct{})
	release := make(chan struct{})
	s, _, stats := fixture(t, c, providerFunc(func(ctx context.Context, q string) (string, error) {
		close(started)
		select {
		case <-release:
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}))
	done := make(chan *dns.Msg)
	go func() { done <- exchange(t, s.Addr(), "tcp", "q.", dns.TypeTXT, -1) }()
	<-started
	r := exchange(t, s.Addr(), "tcp", "q.", dns.TypeTXT, -1)
	if r.Rcode != dns.RcodeServerFailure || stats.Limited.Load() != 1 {
		t.Fatal("unbounded waiters")
	}
	close(release)
	if (<-done).Rcode != 0 {
		t.Fatal("first caller failed")
	}
}
func TestAdmin(t *testing.T) {
	c := testConfig(t)
	c.AdminListen = "127.0.0.1:0"
	s, _, _ := fixture(t, c, providerFunc(func(context.Context, string) (string, error) { return "ok", nil }))
	for _, path := range []string{"/healthz", "/readyz", "/stats"} {
		resp, err := http.Get("http://" + s.AdminAddr() + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || len(b) == 0 {
			t.Fatal("admin response", resp.StatusCode)
		}
		if path == "/stats" && !strings.Contains(string(b), "budget_calls") {
			t.Fatal("stats missing budget")
		}
	}
}
func TestPublicUDPDoesNotFillClientTable(t *testing.T) {
	c := testConfig(t)
	c.ClientEntries = 1
	c.AllowedCIDRs = []string{"0.0.0.0/0"}
	_, h, _ := fixture(t, c, providerFunc(func(context.Context, string) (string, error) { return "ok", nil }))
	q := new(dns.Msg)
	q.SetQuestion("q.", dns.TypeTXT)
	for _, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		w := &recorder{addr: &net.UDPAddr{IP: net.ParseIP(ip), Port: 1234}}
		h.ServeDNS(w, q)
		if w.msg == nil || !w.msg.Truncated {
			t.Fatal("missing TC")
		}
	}
	w := &recorder{addr: &net.TCPAddr{IP: net.ParseIP("203.0.113.1"), Port: 1234}}
	h.ServeDNS(w, q)
	if w.msg == nil || w.msg.Rcode != 0 {
		t.Fatal("spoofed UDP filled client table")
	}
}
func TestMalformedReplyCapabilitiesCleared(t *testing.T) {
	c := testConfig(t)
	s, _, _ := fixture(t, c, providerFunc(func(context.Context, string) (string, error) { t.Error("unexpected provider call"); return "", nil }))
	conn, err := net.Dial("udp", s.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	packet := []byte{0, 1, 4, 0xe0, 0, 0, 0, 0, 0, 0, 0, 0}
	conn.Write(packet)
	b := make([]byte, 512)
	n, err := conn.Read(b)
	if err != nil {
		t.Fatal(err)
	}
	r := new(dns.Msg)
	if err = r.Unpack(b[:n]); err != nil {
		t.Fatal(err)
	}
	if r.RecursionAvailable || r.AuthenticatedData || r.Authoritative || r.Zero || r.Rcode != dns.RcodeFormatError {
		t.Fatal("advertised false capabilities", r)
	}
}
