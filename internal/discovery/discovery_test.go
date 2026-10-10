package discovery

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gregweir/homeHash/internal/drivers/base"
)

/* ---------- helpers ---------- */

// addrOnly turns an httptest URL ("http://127.0.0.1:12345") into the
// "host:port" form VerifyMiner takes.
func addrOnly(url string) string {
	return strings.TrimPrefix(url, "http://")
}

type fakeMethod struct {
	name   string
	miners []base.Miner
	err    error
	called int
}

func (f *fakeMethod) Name() string { return f.name }
func (f *fakeMethod) Discover(context.Context) ([]base.Miner, error) {
	f.called++
	return f.miners, f.err
}

type fakeBrowser struct {
	byService map[string][]ServiceEntry
	browsed   []string
}

func (f *fakeBrowser) Browse(_ context.Context, service, _ string) ([]ServiceEntry, error) {
	f.browsed = append(f.browsed, service)
	return f.byService[service], nil
}

func fakeVerifyOK(string, time.Duration) (bool, error) { return true, nil }

func newMDNSTest(browser ServiceBrowser, verify func(string, time.Duration) (bool, error)) *mDNSDiscovery {
	return &mDNSDiscovery{
		browser:  browser,
		services: []string{"_axeos._sub._http._tcp", "_http._tcp"},
		timeout:  time.Second,
		resolve:  func(string) ([]string, error) { return nil, errors.New("not in test") },
		verify:   verify,
	}
}

/* ---------- Discoverer: merge + dedupe ---------- */

func TestDiscovererDeduplicatesByIP(t *testing.T) {
	a := &fakeMethod{name: "a", miners: []base.Miner{
		{ID: "m1", IP: "192.168.0.10", Driver: "bitaxe"},
		{ID: "m2", IP: "192.168.0.11", Driver: "bitaxe"},
	}}
	b := &fakeMethod{name: "b", miners: []base.Miner{
		{ID: "dup", IP: "192.168.0.10", Driver: "bitaxe"}, // same IP as m1
	}}
	d := NewWithMethods([]DiscoveryMethod{a, b})
	got, err := d.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d miners, want 2 (deduplicated): %v", len(got), got)
	}
}

func TestDiscovererContinuesWhenMethodFails(t *testing.T) {
	broken := &fakeMethod{name: "broken", err: errors.New("mdns socket")}
	working := &fakeMethod{name: "working", miners: []base.Miner{
		{ID: "m1", IP: "192.168.0.10", Driver: "bitaxe"},
	}}
	d := NewWithMethods([]DiscoveryMethod{broken, working})
	got, err := d.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d miners, want 1: %v", len(got), got)
	}
}

/* ---------- mDNS ---------- */

// Real axeOS advertises: instance "Bitaxe <family> <board> (<mac2>)" on
// _http._tcp:80, DNS-SD subtype _axeos._sub._http._tcp, TXT board/family/
// asic/asic_count/fw_version, host <hostname>.local.
func TestMDNSDiscoveryConvertsVerifiedEntriesToMiners(t *testing.T) {
	b := &fakeBrowser{byService: map[string][]ServiceEntry{
		"_axeos._sub._http._tcp": {
			{
				Instance: "Bitaxe Gamma 202 (A1B1)",
				HostName: "Gamma-601.local.",
				Port:     80,
				Addrs:    []string{"192.168.1.10"},
				TXT:      map[string]string{"family": "Bitaxe Gamma", "fw_version": "v2.13.0"},
			},
		},
	}}
	m := newMDNSTest(b, fakeVerifyOK)

	miners, err := m.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(miners) != 1 {
		t.Fatalf("got %d miners, want 1: %+v", len(miners), miners)
	}
	got := miners[0]
	want := base.Miner{
		ID:       "bitaxe-192-168-1-10",
		IP:       "192.168.1.10",
		Model:    "Bitaxe Gamma",
		Firmware: "v2.13.0",
		Driver:   "bitaxe",
	}
	if got != want {
		t.Fatalf("miner = %+v, want %+v", got, want)
	}
	if len(b.browsed) != 2 || b.browsed[0] != "_axeos._sub._http._tcp" || b.browsed[1] != "_http._tcp" {
		t.Fatalf("browsed %v, want subtype then plain fallback", b.browsed)
	}
}

func TestMDNSDiscoveryFiltersNonBitaxeHTTPEntries(t *testing.T) {
	b := &fakeBrowser{byService: map[string][]ServiceEntry{
		"_axeos._sub._http._tcp": {
			{Instance: "Bitaxe GammaTurbo (C1D1)", Port: 80, Addrs: []string{"192.168.1.11"}},
		},
		"_http._tcp": {
			// generic web service on the LAN — not a miner
			{Instance: "Living Room Chromecast", Port: 8008, Addrs: []string{"192.168.8.4"}},
			// a bitaxe that only advertises plain _http._tcp (older firmware)
			{Instance: "Bitaxe Ultra (EE01)", Port: 80, Addrs: []string{"192.168.8.60"}},
		},
	}}
	m := newMDNSTest(b, fakeVerifyOK)

	miners, err := m.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	var ips []string
	for _, m := range miners {
		ips = append(ips, m.IP)
	}
	want := []string{"192.168.1.11", "192.168.8.60"}
	if len(ips) != 2 || ips[0] != want[0] || ips[1] != want[1] {
		t.Fatalf("ips = %v, want %v", ips, want)
	}
}

func TestMDNSDiscoverySkipsUnverifiedAddr(t *testing.T) {
	b := &fakeBrowser{byService: map[string][]ServiceEntry{
		"_axeos._sub._http._tcp": {
			{Instance: "Bitaxe Gamma (A1B1)", Port: 80, Addrs: []string{"192.168.8.99"}},
		},
	}}
	seenAddr := ""
	verify := func(addr string, _ time.Duration) (bool, error) {
		seenAddr = addr
		return false, nil
	}
	m := newMDNSTest(b, verify)

	miners, err := m.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(miners) != 0 {
		t.Fatalf("got %d miners, want 0: %+v", len(miners), miners)
	}
	if seenAddr != "192.168.8.99" {
		t.Fatalf("verify called with %q, want the entry's addr", seenAddr)
	}
}

func TestMDNSDiscoveryResolvesHostnameWithoutAddresses(t *testing.T) {
	b := &fakeBrowser{byService: map[string][]ServiceEntry{
		"_axeos._sub._http._tcp": {
			{Instance: "Bitaxe Gamma (A1B1)", HostName: "gamma601.local.", Port: 80},
		},
	}}
	m := newMDNSTest(b, fakeVerifyOK)
	m.resolve = func(host string) ([]string, error) {
		if host != "gamma601.local" { // trailing dot stripped
			t.Errorf("resolve(%q), want gamma601.local", host)
		}
		return []string{"192.168.8.77"}, nil
	}

	miners, err := m.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(miners) != 1 || miners[0].IP != "192.168.8.77" {
		t.Fatalf("miners = %+v, want one at 192.168.8.77", miners)
	}
}

func TestMDNSDiscoveryNonStandardPortIsKept(t *testing.T) {
	b := &fakeBrowser{byService: map[string][]ServiceEntry{
		"_axeos._sub._http._tcp": {
			{Instance: "Bitaxe Gamma (A1B1)", Port: 8080, Addrs: []string{"192.168.8.20"}},
		},
	}}
	m := newMDNSTest(b, func(addr string, _ time.Duration) (bool, error) {
		return addr == "192.168.8.20:8080", nil
	})

	miners, err := m.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(miners) != 1 || miners[0].IP != "192.168.8.20:8080" {
		t.Fatalf("miners = %+v, want ip:port", miners)
	}
}

/* ---------- IP scan ---------- */

func TestIPScanHostsFromCIDR(t *testing.T) {
	got, err := hostsFromCIDR("192.168.8.0/30")
	if err != nil {
		t.Fatalf("hostsFromCIDR: %v", err)
	}
	want := []string{"192.168.8.1", "192.168.8.2"} // network + broadcast excluded
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("hosts = %v, want %v", got, want)
	}
	if _, err := hostsFromCIDR("not-a-cidr"); err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
}

func TestIPScanDiscoveryFindsRespondingHosts(t *testing.T) {
	var mu sync.Mutex
	var probed []string
	prober := func(addr string, _ time.Duration) (bool, error) {
		mu.Lock()
		probed = append(probed, addr)
		mu.Unlock()
		return addr == "192.168.8.1:80", nil
	}
	i := &IPScanDiscovery{
		cidrs:       []string{"192.168.8.0/30"},
		prober:      prober,
		concurrency: 4,
		timeout:     100 * time.Millisecond,
	}

	miners, err := i.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(miners) != 1 || miners[0].ID != "bitaxe-192-168-8-1" || miners[0].IP != "192.168.8.1" {
		t.Fatalf("miners = %+v, want bitaxe-192-168-8-1", miners)
	}
	if miners[0].Model != "Bitaxe (auto)" || miners[0].Driver != "bitaxe" {
		t.Fatalf("miner = %+v, want auto model + bitaxe driver", miners[0])
	}
	if len(probed) != 2 {
		t.Fatalf("probed %v, want both hosts", probed)
	}
}

/* ---------- local subnet detection ---------- */

func TestLocalSubnetsFiltersNonPrivateAddrs(t *testing.T) {
	got := localSubnets([]string{
		"127.0.0.1/8",     // loopback
		"169.254.10.5/16", // link-local
		"192.168.8.42/24", // home LAN
		"10.9.9.9/8",      // private
		"fe80::abcd/64",   // IPv6 link-local
		"203.0.113.7/24",  // documentation range: public, skip
	})
	want := []string{"192.168.8.0/24", "10.9.9.0/24"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("subnets = %v, want %v", got, want)
	}
}

/* ---------- default prober: VerifyMiner ---------- */

func TestVerifyMinerDetectsSystemInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/system/info" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"hostname":"Gamma 601","hashRate":1191.4,"version":"v2.13.0"}`))
	}))
	defer srv.Close()

	ok, err := VerifyMiner(addrOnly(srv.URL), 2*time.Second)
	if err != nil {
		t.Fatalf("VerifyMiner: %v", err)
	}
	if !ok {
		t.Fatal("VerifyMiner = false, want true")
	}

	ok, err = VerifyMiner("127.0.0.1:1", 200*time.Millisecond)
	if err != nil {
		t.Logf("VerifyMiner error (expected for closed port): %v", err)
	}
	if ok {
		t.Fatal("VerifyMiner = true for closed port, want false")
	}
}

// An axeOS-lookalike 200 from a router or other web device (HTML body)
// must NOT pass verification: we confirm a real axeOS JSON payload.
func TestVerifyMinerRejectsHTML200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>WAN Settings</body></html>"))
	}))
	defer srv.Close()

	ok, err := VerifyMiner(addrOnly(srv.URL), 2*time.Second)
	if err != nil {
		t.Fatalf("VerifyMiner: %v", err)
	}
	if ok {
		t.Fatal("VerifyMiner accepted a 200/HTML router page, want false")
	}
}

func TestVerifyMinerAcceptsAxeOSJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"hostname":"Gamma 601","hashRate":1191,"version":"v2.13.0"}`))
	}))
	defer srv.Close()

	ok, err := VerifyMiner(addrOnly(srv.URL), 2*time.Second)
	if err != nil {
		t.Fatalf("VerifyMiner: %v", err)
	}
	if !ok {
		t.Fatal("VerifyMiner rejected an axeOS JSON body, want true")
	}
}

func TestIPScanSkipsSelfAddresses(t *testing.T) {
	var mu sync.Mutex
	var probed []string
	prober := func(addr string, _ time.Duration) (bool, error) {
		mu.Lock()
		probed = append(probed, addr)
		mu.Unlock()
		return true, nil
	}
	i := &IPScanDiscovery{
		cidrs:       []string{"192.168.1.0/24"},
		selfIPs:     []string{"192.168.1.65"}, // the running host itself
		prober:      prober,
		concurrency: 4,
		timeout:     10 * time.Millisecond,
	}
	if _, err := i.Discover(context.Background()); err != nil {
		t.Fatalf("Discover: %v", err)
	}
	for _, addr := range probed {
		if strings.HasPrefix(addr, "192.168.1.65") {
			t.Fatalf("probed own address %q", addr)
		}
	}
	if len(probed) != 253 { // /24 minus its own host
		t.Fatalf("probed %d addresses, want 253", len(probed))
	}
}
