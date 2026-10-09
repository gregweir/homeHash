package discovery

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/gregweir/homeHash/internal/drivers/base"
)

/* ---------- IP range scan fallback ---------- */

// IPScanDiscovery probes CIDR ranges for hosts that answer like an axeOS
// web UI. Used where mDNS is unavailable (resolvers that ignore it, or
// firmware that isn't advertising).
type IPScanDiscovery struct {
	cidrs       []string                                               // nil: subnets of local interfaces
	selfIPs     []string                                               // nil: this host's interface addresses
	prober      func(addr string, timeout time.Duration) (bool, error) // nil: VerifyMiner
	concurrency int                                                    // default 32
	timeout     time.Duration                                          // per-address, default 1s
}

func newIPScan() *IPScanDiscovery {
	return &IPScanDiscovery{concurrency: 32, timeout: time.Second}
}

func (i *IPScanDiscovery) Name() string { return "IPScan" }

func (i *IPScanDiscovery) Discover(ctx context.Context) ([]base.Miner, error) {
	cidrs := i.cidrs
	if len(cidrs) == 0 {
		cidrs = localSubnets(interfaceCIDRs())
	}

	var hosts []string
	for _, cidr := range cidrs {
		hs, err := hostsFromCIDR(cidr)
		if err != nil {
			continue // malformed input must not kill the scan
		}
		hosts = append(hosts, hs...)
	}

	// Never probe this machine's own addresses; a web server on the host
	// answering 200 would otherwise surface itself as "a miner".
	self := i.selfIPs
	if len(self) == 0 {
		self = interfaceAddrs()
	}
	selfSet := make(map[string]bool, len(self))
	for _, ip := range self {
		selfSet[ip] = true
	}
	var scan []string
	for _, h := range hosts {
		if !selfSet[h] {
			scan = append(scan, h)
		}
	}
	if len(scan) == 0 {
		return nil, nil
	}

	prober := i.prober
	if prober == nil {
		prober = VerifyMiner
	}
	workers := i.concurrency
	if workers <= 0 {
		workers = 32
	}
	timeout := i.timeout
	if timeout <= 0 {
		timeout = time.Second
	}

	probed := make(chan string, workers)
	var out []base.Miner
	var mu sync.Mutex
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for addr := range probed {
				if ctx.Err() != nil {
					return
				}
				ok, err := prober(addr, timeout)
				if err != nil || !ok {
					continue
				}
				mgn := base.Miner{
					ID:     "bitaxe-" + sanitizeIP(hostPart(addr)),
					IP:     hostPart(addr),
					Driver: "bitaxe",
					Model:  "Bitaxe (auto)",
				}
				mu.Lock()
				out = append(out, mgn)
				mu.Unlock()
			}
		}()
	}
	for _, h := range scan {
		select {
		case probed <- h + ":80":
		case <-ctx.Done():
			close(probed)
			wg.Wait()
			return nil, ctx.Err()
		}
	}
	close(probed)
	wg.Wait()
	return out, nil
}

// hostPart keeps only the address part of "ip[:port]".
func hostPart(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// hostsFromCIDR expands a CIDR into usable host addresses. Ranges with two or
// more host bits drop the network and broadcast addresses; /31s (RFC 3021)
// keep both of theirs.
func hostsFromCIDR(cidr string) ([]string, error) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, fmt.Errorf("parse prefix %q: %w", cidr, err)
	}
	p = p.Masked()
	hostBits := p.Addr().BitLen() - p.Bits()

	switch {
	case hostBits <= 1:
		return []string{p.Addr().String(), lastAddr(p).String()}, nil
	default:
		first := p.Addr().Next()   // skip the network address
		last := lastAddr(p).Prev() // skip the broadcast address
		if !first.IsValid() || !last.IsValid() {
			return nil, nil
		}
		var out []string
		for a := first; ; a = a.Next() {
			out = append(out, a.String())
			if a == last {
				break
			}
		}
		return out, nil
	}
}

// lastAddr returns the highest address in prefix p.
func lastAddr(p netip.Prefix) netip.Addr {
	a := p.Addr()
	hostBits := a.BitLen() - p.Bits()
	if a.Is4() {
		b := a.As4()
		for bit := hostBits - 1; bit >= 0; bit-- {
			b[3-bit/8] |= 1 << (bit % 8)
		}
		return netip.AddrFrom4(b)
	}
	b := a.As16()
	for bit := hostBits - 1; bit >= 0; bit-- {
		b[15-bit/8] |= 1 << (bit % 8)
	}
	return netip.AddrFrom16(b)
}

// interfaceAddrs returns every address string on the host's interfaces.
func interfaceAddrs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				out = append(out, ipnet.IP.String())
			}
		}
	}
	return out
}

// interfaceCIDRs returns the CIDR-strings of the host's network interfaces.
func interfaceCIDRs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				out = append(out, ipnet.String())
			}
		}
	}
	return out
}

// localSubnets keeps private-range CIDRs reduced to /24s (typical home
// subnets), deduplicated in input order.
func localSubnets(cidrs []string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, cidr := range cidrs {
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			continue
		}
		addr := p.Addr()
		if !addr.Is4() || !addr.IsPrivate() {
			continue
		}
		sub := netip.PrefixFrom(addr, 24).Masked().String()
		if !seen[sub] {
			seen[sub] = true
			out = append(out, sub)
		}
	}
	return out
}
