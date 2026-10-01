package service

import (
	"net"
	"net/netip"
	"sync"
	"time"

	"ask53/internal/config"
)

type bucket struct {
	tokens float64
	last   time.Time
	rate   config.Rate
}

func newBucket(r config.Rate, now time.Time) bucket { return bucket{float64(r.Burst), now, r} }
func (b *bucket) refill(now time.Time) {
	if now.After(b.last) {
		b.tokens = min(float64(b.rate.Burst), b.tokens+now.Sub(b.last).Seconds()*b.rate.PerSecond)
		b.last = now
	}
}
func (b *bucket) take(now time.Time) bool {
	b.refill(now)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type clientLimit struct {
	requests, work bucket
	last           time.Time
}

// Gate bounds both rate and the memory needed for client accounting. A full table
// rejects new clients rather than evicting active buckets and resetting limits.
type Gate struct {
	mu       sync.Mutex
	cfg      config.Config
	networks []netip.Prefix
	ingress  bucket
	clients  map[netip.Prefix]*clientLimit
	sweep    time.Time
}

func NewGate(c config.Config) *Gate {
	g := &Gate{cfg: c, ingress: newBucket(c.IngressRate, time.Now()), clients: make(map[netip.Prefix]*clientLimit)}
	for _, s := range c.AllowedCIDRs {
		p, _ := netip.ParsePrefix(s)
		g.networks = append(g.networks, p.Masked())
	}
	return g
}
func Address(addr net.Addr) (netip.Addr, bool) {
	switch a := addr.(type) {
	case *net.UDPAddr:
		return a.AddrPort().Addr().Unmap(), true
	case *net.TCPAddr:
		return a.AddrPort().Addr().Unmap(), true
	}
	if addr == nil {
		return netip.Addr{}, false
	}
	s, _, e := net.SplitHostPort(addr.String())
	a, ae := netip.ParseAddr(s)
	return a.Unmap(), e == nil && ae == nil
}
func (g *Gate) Allowed(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range g.networks {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Ingress is applied before UDP parsing/goroutine creation, and before admitting
// each TCP connection. Packet floods still need filtering at the firewall.
func (g *Gate) Ingress(a netip.Addr) bool {
	if !g.Allowed(a) {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ingress.take(time.Now())
}
func prefix(a netip.Addr) netip.Prefix {
	a = a.Unmap()
	bits := 32
	if a.Is6() {
		bits = 64
	}
	return netip.PrefixFrom(a, bits).Masked()
}
func (g *Gate) Request(a netip.Addr) bool { return g.take(a, false, time.Now()) }
func (g *Gate) Work(a netip.Addr) bool    { return g.take(a, true, time.Now()) }
func (g *Gate) take(a netip.Addr, work bool, now time.Time) bool {
	if !g.Allowed(a) {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.Sub(g.sweep) >= time.Second {
		for k, v := range g.clients {
			if now.Sub(v.last) > g.cfg.ClientIdleTTL.Value() {
				v.requests.refill(now)
				v.work.refill(now)
				if v.requests.tokens >= float64(v.requests.rate.Burst) && v.work.tokens >= float64(v.work.rate.Burst) {
					delete(g.clients, k)
				}
			}
		}
		g.sweep = now
	}
	key := prefix(a)
	v := g.clients[key]
	if v == nil {
		if len(g.clients) >= g.cfg.ClientEntries {
			return false
		}
		v = &clientLimit{newBucket(g.cfg.ClientRate, now), newBucket(g.cfg.ClientAIRate, now), now}
		g.clients[key] = v
	}
	v.last = now
	if work {
		return v.work.take(now)
	}
	return v.requests.take(now)
}
