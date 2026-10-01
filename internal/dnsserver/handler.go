package dnsserver

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"ask53/internal/config"
	"ask53/internal/service"
	"github.com/miekg/dns"
)

const maxUDP = 1232

type Handler struct {
	cfg    config.Config
	engine *service.Engine
	gate   *service.Gate
	stats  *service.Stats
	slots  chan struct{}
}

func NewHandler(c config.Config, e *service.Engine, g *service.Gate, s *service.Stats) *Handler {
	return &Handler{c, e, g, s, make(chan struct{}, c.MaxHandlers)}
}
func (h *Handler) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	h.stats.Requests.Add(1)
	a, ok := service.Address(w.RemoteAddr())
	_, tcp := w.RemoteAddr().(*net.TCPAddr)
	if !ok || !h.gate.Allowed(a) {
		h.stats.Denied.Add(1)
		if tcp {
			w.Close()
		}
		return
	}
	// Public UDP is spoofable: do not let it allocate client rate-table entries.
	if (tcp || a.IsLoopback()) && !h.gate.Request(a) {
		h.stats.Limited.Add(1)
		if tcp {
			w.Close()
		}
		return
	}
	m := new(dns.Msg)
	m.SetReply(req)
	m.RecursionAvailable = false
	m.AuthenticatedData = false
	m.Authoritative = false
	m.Compress = true
	if req.Response {
		return
	}
	fail := func(code int) { m.Rcode = code; h.write(w, m, req, tcp) }
	if req.Opcode != dns.OpcodeQuery {
		fail(dns.RcodeNotImplemented)
		return
	}
	if len(req.Question) != 1 || len(req.Answer) > 0 || len(req.Ns) > 0 || len(req.Extra) > 1 || req.Truncated || req.Zero || req.Rcode != dns.RcodeSuccess {
		h.stats.Malformed.Add(1)
		m.Question = nil
		fail(dns.RcodeFormatError)
		return
	}
	opt := req.IsEdns0()
	if len(req.Extra) > 0 && (opt == nil || opt.Hdr.Name != ".") {
		h.stats.Malformed.Add(1)
		fail(dns.RcodeFormatError)
		return
	}
	if opt != nil {
		m.SetEdns0(maxUDP, false)
		if opt.Version() != 0 {
			fail(dns.RcodeBadVers)
			return
		}
	}
	q := req.Question[0]
	if q.Qclass != dns.ClassINET || q.Qtype != dns.TypeTXT {
		fail(dns.RcodeRefused)
		return
	}
	question, e := Question(q.Name, h.cfg.Zone)
	if e != nil {
		fail(dns.RcodeRefused)
		return
	}
	// Never compute or reflect TXT answers to an unverified public UDP source.
	if !tcp && (h.cfg.UDPMode == "tcp-only" || !a.IsLoopback()) {
		m.Truncated = true
		h.stats.Truncated.Add(1)
		h.write(w, m, req, false)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		h.stats.Limited.Add(1)
		fail(dns.RcodeServerFailure)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.RequestTimeout.Value())
	defer cancel()
	result, e := h.engine.Get(ctx, question, a)
	if e != nil {
		fail(dns.RcodeServerFailure)
		return
	}
	ttl := max(int64(0), int64(time.Until(result.Expires)/time.Second))
	m.Answer = []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: uint32(ttl)}, Txt: txtStrings(result.Text)}}
	h.stats.Answers.Add(1)
	h.write(w, m, req, tcp)
}
func Question(name, zone string) (string, error) {
	if !config.ValidName(name) {
		return "", errors.New("unsupported name")
	}
	name = strings.ToLower(name)
	if zone != "" {
		if name == zone || !strings.HasSuffix(name, "."+zone) {
			return "", errors.New("outside namespace")
		}
		name = strings.TrimSuffix(name, zone)
	}
	question := strings.ReplaceAll(strings.TrimSuffix(name, "."), ".", " ")
	if question == "" {
		return "", errors.New("empty question")
	}
	return question, nil
}

// TXT strings are limited to 255 wire bytes. Split only at UTF-8 boundaries,
// and escape backslashes for miekg/dns's presentation-format string API.
func txtStrings(s string) []string {
	var out []string
	for len(s) > 0 {
		n := min(255, len(s))
		for n < len(s) && !utf8.RuneStart(s[n]) {
			n--
		}
		out = append(out, strings.ReplaceAll(s[:n], "\\", "\\\\"))
		s = s[n:]
	}
	return out
}
func (h *Handler) write(w dns.ResponseWriter, m, req *dns.Msg, tcp bool) {
	if !tcp {
		limit := dns.MinMsgSize
		if opt := req.IsEdns0(); opt != nil {
			limit = max(dns.MinMsgSize, min(maxUDP, int(opt.UDPSize())))
		}
		b, e := m.Pack()
		if e != nil {
			h.stats.WriteErrors.Add(1)
			return
		}
		if len(b) > limit {
			m.Answer = nil
			m.Ns = nil
			m.Truncated = true
			h.stats.Truncated.Add(1)
		}
	}
	if e := w.WriteMsg(m); e != nil {
		h.stats.WriteErrors.Add(1)
	}
}
