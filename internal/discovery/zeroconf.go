package discovery

import (
	"context"
	"fmt"
	"strings"

	zc "github.com/grandcat/zeroconf"
)

// zeroconfBrowser implements ServiceBrowser via github.com/grandcat/zeroconf.
type zeroconfBrowser struct{}

// defaultBrowser returns the production mDNS implementation.
func defaultBrowser() ServiceBrowser { return zeroconfBrowser{} }

// Browse queries the local link for the given DNS-SD service until ctx ends,
// which the caller should bound with a timeout.
func (zeroconfBrowser) Browse(ctx context.Context, service, domain string) ([]ServiceEntry, error) {
	resolver, err := zc.NewResolver(nil)
	if err != nil {
		return nil, fmt.Errorf("mDNS resolver: %w", err)
	}
	ch := make(chan *zc.ServiceEntry, 16)
	if err := resolver.Browse(ctx, service, domain, ch); err != nil {
		return nil, fmt.Errorf("browse %s: %w", service, err)
	}

	var out []ServiceEntry
	for e := range ch {
		addrs := make([]string, 0, len(e.AddrIPv4))
		for _, ip := range e.AddrIPv4 {
			addrs = append(addrs, ip.String())
		}
		txt := make(map[string]string, len(e.Text))
		for _, rec := range e.Text {
			if k, v, ok := strings.Cut(rec, "="); ok {
				txt[k] = v
			}
		}
		out = append(out, ServiceEntry{
			Instance: e.Instance,
			HostName: strings.TrimSuffix(e.HostName, "."),
			Port:     e.Port,
			Addrs:    addrs,
			TXT:      txt,
		})
	}
	return out, nil
}
