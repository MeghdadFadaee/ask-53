package dnsserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeghdadFadaee/ask-53/internal/config"
	"github.com/MeghdadFadaee/ask-53/internal/service"
	"github.com/miekg/dns"
)

type packetConn struct {
	writes chan struct{}
	net.PacketConn
	gate  *service.Gate
	stats *service.Stats
}

func (p *packetConn) WriteTo(b []byte, a net.Addr) (int, error) {
	select {
	case p.writes <- struct{}{}:
		defer func() { <-p.writes }()
	default:
		return 0, errors.New("UDP write capacity reached")
	}
	if err := p.PacketConn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return 0, err
	}
	return p.PacketConn.WriteTo(b, a)
}
func (p *packetConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, a, e := p.PacketConn.ReadFrom(b)
		if e != nil {
			return n, a, e
		}
		ip, ok := service.Address(a)
		if !ok || !p.gate.Ingress(ip) {
			p.stats.Denied.Add(1)
			continue
		}
		if n == len(b) {
			p.stats.Malformed.Add(1)
			continue
		}
		return n, a, nil
	}
}

type listener struct {
	net.Listener
	gate  *service.Gate
	slots chan struct{}
	stats *service.Stats
}

func (l *listener) Accept() (net.Conn, error) {
	for {
		c, e := l.Listener.Accept()
		if e != nil {
			return nil, e
		}
		a, ok := service.Address(c.RemoteAddr())
		if !ok || !l.gate.Ingress(a) {
			l.stats.Denied.Add(1)
			c.Close()
			continue
		}
		select {
		case l.slots <- struct{}{}:
			return &limitedConn{Conn: c, release: func() { <-l.slots }}, nil
		default:
			l.stats.Limited.Add(1)
			c.Close()
		}
	}
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Write(b []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}
func (c *limitedConn) Close() error { e := c.Conn.Close(); c.once.Do(c.release); return e }

// TCP DNS frames are inherently capped at 65535 bytes by the library. Reject
// frames above our request limit before parsing their records.
type boundedReader struct {
	dns.Reader
	packet dns.PacketConnReader
	stats  *service.Stats
}

func (b *boundedReader) ReadPacketConn(c net.PacketConn, t time.Duration) ([]byte, net.Addr, error) {
	return b.packet.ReadPacketConn(c, t)
}
func (b *boundedReader) ReadTCP(c net.Conn, t time.Duration) ([]byte, error) {
	m, e := b.Reader.ReadTCP(c, t)
	if e == nil && len(m) >= maxUDP {
		b.stats.Malformed.Add(1)
		return nil, errors.New("DNS request too large")
	}
	return m, e
}

// Clear capability bits even on early library-generated malformed replies.
type safeWriter struct{ dns.Writer }

func (w safeWriter) Write(b []byte) (int, error) {
	if len(b) >= 4 {
		b[2] &= ^byte(4)
		b[3] &= ^byte(0xe0)
	}
	return w.Writer.Write(b)
}

type Server struct {
	adminAddr string
	udp, tcp  *dns.Server
	admin     *http.Server
	addr      string
	errs      chan error
	ready     atomic.Bool
}

func (s *Server) AdminAddr() string    { return s.adminAddr }
func (s *Server) Addr() string         { return s.addr }
func (s *Server) Errors() <-chan error { return s.errs }
func Start(c config.Config, e *service.Engine, g *service.Gate, stats *service.Stats) (*Server, error) {
	tcp, e1 := net.Listen("tcp", c.Listen)
	if e1 != nil {
		return nil, e1
	}
	udp, e1 := net.ListenPacket("udp", tcp.Addr().String())
	if e1 != nil {
		tcp.Close()
		return nil, e1
	}
	var admin net.Listener
	if c.AdminListen != "" {
		admin, e1 = net.Listen("tcp", c.AdminListen)
		if e1 != nil {
			tcp.Close()
			udp.Close()
			return nil, e1
		}
	}
	s := &Server{addr: tcp.Addr().String(), errs: make(chan error, 3)}
	handler := NewHandler(c, e, g, stats)
	started := make(chan struct{}, 2)
	makeServer := func() *dns.Server {
		return &dns.Server{Handler: handler, UDPSize: maxUDP, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: func() time.Duration { return c.TCPIdleTimeout.Value() }, MaxTCPQueries: 100,
			NotifyStartedFunc: func() { started <- struct{}{} },
			MsgAcceptFunc: func(h dns.Header) dns.MsgAcceptAction {
				if h.Bits&0x8000 != 0 {
					return dns.MsgIgnore
				}
				if int(h.Bits>>11)&15 != dns.OpcodeQuery {
					return dns.MsgRejectNotImplemented
				}
				if h.Qdcount != 1 || h.Ancount != 0 || h.Nscount != 0 || h.Arcount > 1 {
					stats.Malformed.Add(1)
					return dns.MsgReject
				}
				return dns.MsgAccept
			},
			MsgInvalidFunc: func([]byte, error) { stats.Malformed.Add(1) },
			DecorateWriter: func(w dns.Writer) dns.Writer { return safeWriter{w} },
			DecorateReader: func(r dns.Reader) dns.Reader { p, _ := r.(dns.PacketConnReader); return &boundedReader{r, p, stats} },
		}
	}
	s.udp = makeServer()
	s.udp.PacketConn = &packetConn{PacketConn: udp, gate: g, stats: stats, writes: make(chan struct{}, 64)}
	s.tcp = makeServer()
	s.tcp.Listener = &listener{tcp, g, make(chan struct{}, c.MaxTCPConnections), stats}
	go func() { s.errs <- s.udp.ActivateAndServe() }()
	go func() { s.errs <- s.tcp.ActivateAndServe() }()
	// Both sockets are bound before either service starts. Observe library startup
	// before returning so immediate shutdown cannot race initialization.
	for range 2 {
		select {
		case <-started:
		case err := <-s.errs:
			tcp.Close()
			udp.Close()
			if admin != nil {
				admin.Close()
			}
			return nil, fmt.Errorf("DNS startup: %w", err)
		}
	}
	s.ready.Store(true)
	if admin != nil {
		s.adminAddr = admin.Addr().String()
		mux := http.NewServeMux()
		mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintln(w, "ok")
		})
		mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
			if !s.ready.Load() {
				http.Error(w, "draining", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintln(w, "ready")
		})
		mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"counters": stats.Snapshot(), "state": e.Snapshot()})
		})
		s.admin = &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
		go func() {
			err := s.admin.Serve(admin)
			if !errors.Is(err, http.ErrServerClosed) {
				s.errs <- err
			}
		}()
	}
	return s, nil
}
func (s *Server) Close(ctx context.Context) error {
	s.ready.Store(false)
	errs := make(chan error, 3)
	n := 2
	go func() { errs <- s.udp.ShutdownContext(ctx) }()
	go func() { errs <- s.tcp.ShutdownContext(ctx) }()
	if s.admin != nil {
		n++
		go func() {
			err := s.admin.Shutdown(ctx)
			if err != nil {
				s.admin.Close()
			}
			errs <- err
		}()
	}
	var result error
	for range n {
		result = errors.Join(result, <-errs)
	}
	return result
}
