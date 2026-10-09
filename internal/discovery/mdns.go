package discovery

import (
	"context"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/gregweir/homeHash/internal/drivers/base"
)

/* ---------- mDNS (DNS-SD) ---------- */

// ServiceEntry is a normalized DNS-SD service entry.
type ServiceEntry struct {
	Instance string // e.g. "Bitaxe Gamma 202 (A1B1)"
	HostName string // e.g. "gamma601.local."
	Port     int
	Addrs    []string          // IPv4 address strings; may be empty
	TXT      map[string]string // decoded key=value pairs, e.g. {family: "Bitaxe Gamma"}
}

// ServiceBrowser browses DNS-SD services on the local link.
type ServiceBrowser interface {
	Browse(ctx context.Context, service, domain string) ([]ServiceEntry, error)
}

// mDNSDiscovery finds Bitaxe miners published by esp-miner's mDNS stack:
//
//	instance "Bitaxe <family> <board> (<mac-suffix>)"
//	service  _http._tcp on port 80, DNS-SD subtype _axeos._sub._http._tcp
//	TXT      board, family, asic, asic_count, fw_version
//
// The subtype query is precise; plain _http._tcp is a fallback that only
// accepts entries whose instance name looks like a Bitaxe. Every candidate is
// verified with a real HTTP call before being claimed.
type mDNSDiscovery struct {
	browser  ServiceBrowser                                         // nil: zeroconf default
	services []string                                               // service types queried, in order
	timeout  time.Duration                                          // browse window per service
	resolve  func(host string) ([]string, error)                    // nil: net resolver
	verify   func(addr string, timeout time.Duration) (bool, error) // nil: VerifyMiner
}

func newMDNS() *mDNSDiscovery {
	return &mDNSDiscovery{
		services: []string{"_axeos._sub._http._tcp", "_http._tcp"},
		timeout:  2500 * time.Millisecond,
	}
}

func (m *mDNSDiscovery) Name() string { return "mDNS" }

func (m *mDNSDiscovery) Discover(ctx context.Context) ([]base.Miner, error) {
	if m.browser == nil {
		m.browser = defaultBrowser()
	}
	var out []base.Miner
	// Always browse both services: a mixed LAN can hold new-firmware Bitaxes
	// (subtype) and older ones advertising only plain _http._tcp.
	for _, svc := range m.services {
		plain := strings.EqualFold(svc, "_http._tcp")
		bctx, cancel := context.WithTimeout(ctx, m.timeout)
		entries, err := m.browser.Browse(bctx, svc, "local.")
		cancel()
		if err != nil {
			log.Printf("mDNS browse %s: %v", svc, err)
			continue
		}
		for _, e := range entries {
			if plain && !isBitaxeEntry(e) {
				continue
			}
			if mgn, ok := m.claim(e, ctx); ok {
				out = append(out, mgn)
			}
		}
	}
	return dedupe(out), nil
}

// isBitaxeEntry reports whether a plain _http._tcp entry is a Bitaxe.
func isBitaxeEntry(e ServiceEntry) bool {
	lower := strings.ToLower(e.Instance)
	return strings.HasPrefix(lower, "bitaxe") ||
		strings.HasPrefix(strings.ToLower(e.TXT["family"]), "bitaxe")
}

// claim turns a candidate entry into a miner, verifying it over HTTP first.
func (m *mDNSDiscovery) claim(e ServiceEntry, ctx context.Context) (base.Miner, bool) {
	ips := e.Addrs
	if len(ips) == 0 && e.HostName != "" {
		if m.resolve == nil {
			m.resolve = resolveMDNSHost
		}
		host := strings.TrimSuffix(e.HostName, ".")
		if resolved, err := m.resolve(host); err == nil && len(resolved) > 0 {
			ips = resolved
		}
	}
	if len(ips) == 0 {
		return base.Miner{}, false
	}
	sort.Strings(ips)
	addr := ips[0]
	if e.Port != 0 && e.Port != 80 {
		addr = net.JoinHostPort(addr, fmt.Sprintf("%d", e.Port))
	}

	if m.verify == nil {
		m.verify = VerifyMiner
	}
	ok, err := m.verify(addr, 2*time.Second)
	if err != nil || !ok {
		// Entries can point at hardware that is switching APs or mid-reboot.
		return base.Miner{}, false
	}

	mgn := base.Miner{
		ID:     "bitaxe-" + sanitizeIP(netipHostPart(addr)),
		IP:     addr,
		Driver: "bitaxe",
		Model:  "Bitaxe (auto)",
	}
	if fam := e.TXT["family"]; fam != "" {
		mgn.Model = fam
	}
	if fw := e.TXT["fw_version"]; fw != "" {
		mgn.Firmware = fw
	}
	return mgn, true
}

// netipHostPart splits an "ip" or "ip:port" address, keeping the host part.
func netipHostPart(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

func sanitizeIP(s string) string {
	return strings.NewReplacer(".", "-", ":", "-").Replace(s)
}

func resolveMDNSHost(host string) ([]string, error) {
	r := net.Resolver{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addrs, err := r.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	var ips []string
	for _, a := range addrs {
		if _, _, err := net.SplitHostPort(a); err == nil {
			continue // no host:port here
		}
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			ips = append(ips, ip.String())
		}
	}
	return ips, nil
}

func dedupe(miners []base.Miner) []base.Miner {
	seen := make(map[string]bool, len(miners))
	out := miners[:0]
	for _, m := range miners {
		if !seen[m.IP] {
			seen[m.IP] = true
			out = append(out, m)
		}
	}
	return out
}
