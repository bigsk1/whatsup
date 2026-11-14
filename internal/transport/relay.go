package transport

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"

	"whatsup/internal/transport/address"
)

const (
	MaxRelayMapBytes = 1 << 20
	maxRelayRegions  = 64
	maxRelayNodes    = 128
	relayProfileTTL  = time.Hour
)

var ErrRelayProfile = errors.New("relay configuration is unavailable or not approved")

// RelayProfile is an immutable operator-selected map. It is not a peer-provided
// setting. Addresses are resolved and pinned before Tailcat receives any region;
// neither a peer's embedded map nor an inherited proxy may add an outbound host.
type RelayProfile struct{ state *relayProfileState }

type relayProfileState struct {
	source        string
	expires       time.Time
	regions       map[tailcfg.DERPRegionID]*tailcfg.DERPRegion
	wire          map[tailcfg.DERPRegionID]*tailcfg.DERPRegion
	defaultRegion tailcfg.DERPRegionID
}

func (RelayProfile) String() string               { return "[private WhatsUp relay configuration]" }
func (RelayProfile) GoString() string             { return "[private WhatsUp relay configuration]" }
func (RelayProfile) MarshalJSON() ([]byte, error) { return nil, ErrRelayProfile }

func (p *RelayProfile) Ready() bool {
	return p != nil && p.state != nil && time.Now().Before(p.state.expires) &&
		p.state.defaultRegion.IsValid() && p.state.regions[p.state.defaultRegion] != nil
}

// FetchRelayProfile contacts only an explicit HTTPS operator URL. It never
// selects Tailcat's public default, follows redirects, uses environment proxies,
// disables TLS checks, accepts private destinations, or returns raw HTTP errors.
func FetchRelayProfile(ctx context.Context, rawURL string) (*RelayProfile, error) {
	if ctx == nil {
		return nil, ErrRelayProfile
	}
	u, err := relayMapURL(rawURL)
	if err != nil {
		return nil, err
	}
	work, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resolve := publicResolver(net.DefaultResolver)
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(target)
			if err != nil || host != u.Hostname() || port != u.Port() && !(u.Port() == "" && port == "443") {
				return nil, ErrRelayProfile
			}
			ips, err := resolve(ctx, host)
			if err != nil {
				return nil, ErrRelayProfile
			}
			for _, ip := range ips {
				connection, err := (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return connection, nil
				}
			}
			return nil, ErrRelayProfile
		},
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
		DisableKeepAlives:      true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRelayProfile }}
	request, err := http.NewRequestWithContext(work, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrRelayProfile
	}
	// No application/user identifier or server/client-role hint is submitted.
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "")
	response, err := client.Do(request)
	if err != nil {
		return nil, ErrRelayProfile
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > MaxRelayMapBytes {
		return nil, ErrRelayProfile
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxRelayMapBytes+1))
	if err != nil || len(body) == 0 || len(body) > MaxRelayMapBytes {
		return nil, ErrRelayProfile
	}
	var model tailcfg.DERPMap
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&model) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, ErrRelayProfile
	}
	return reviewedRelayProfile(work, u.String(), &model, resolve)
}

func relayMapURL(raw string) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > 2048 {
		return nil, ErrRelayProfile
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || u.Opaque != "" || !relayHost(u.Hostname()) {
		return nil, ErrRelayProfile
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
			return nil, ErrRelayProfile
		}
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicRelayIP(ip) {
		return nil, ErrRelayProfile
	}
	return u, nil
}

type relayResolve func(context.Context, string) ([]netip.Addr, error)

func publicResolver(resolver *net.Resolver) relayResolve {
	return func(ctx context.Context, host string) ([]netip.Addr, error) {
		if !relayHost(host) {
			return nil, ErrRelayProfile
		}
		var ips []netip.Addr
		if ip, err := netip.ParseAddr(host); err == nil {
			ips = []netip.Addr{ip}
		} else {
			var err error
			ips, err = resolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, ErrRelayProfile
			}
		}
		if len(ips) == 0 || len(ips) > 16 {
			return nil, ErrRelayProfile
		}
		for _, ip := range ips {
			if !publicRelayIP(ip) {
				return nil, ErrRelayProfile
			}
		}
		return ips, nil
	}
}

var reservedRelayNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2001::/32"), netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("2001:20::/28"), netip.MustParsePrefix("2002::/16"),
}

func publicRelayIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" || ip.Is4In6() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range reservedRelayNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func relayHost(host string) bool {
	if len(host) == 0 || len(host) > 253 || strings.HasSuffix(host, ".") {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == ""
	}
	if !strings.Contains(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if c < 'a' || c > 'z' {
				if c < '0' || c > '9' {
					if c != '-' {
						return false
					}
				}
			}
		}
	}
	return true
}

func cloneRelayRegion(region *tailcfg.DERPRegion) *tailcfg.DERPRegion {
	copyRegion := *region
	copyRegion.Nodes = make([]*tailcfg.DERPNode, len(region.Nodes))
	for i, node := range region.Nodes {
		if node == nil {
			continue
		}
		copyNode := *node
		copyRegion.Nodes[i] = &copyNode
	}
	return &copyRegion
}

func reviewedRelayProfile(ctx context.Context, source string, model *tailcfg.DERPMap, resolve relayResolve) (*RelayProfile, error) {
	if model == nil || len(model.Regions) == 0 || len(model.Regions) > maxRelayRegions || resolve == nil {
		return nil, ErrRelayProfile
	}
	state := &relayProfileState{source: source, expires: time.Now().Add(relayProfileTTL),
		regions: make(map[tailcfg.DERPRegionID]*tailcfg.DERPRegion), wire: make(map[tailcfg.DERPRegionID]*tailcfg.DERPRegion)}
	var ids []tailcfg.DERPRegionID
	nodes := 0
	names := make(map[string]bool)
	for id, region := range model.Regions {
		if region == nil || !id.IsValid() || region.RegionID != id || len(region.Nodes) == 0 || len(region.Nodes) > 8 || len(region.RegionCode) > 32 || len(region.RegionName) > 128 {
			return nil, ErrRelayProfile
		}
		// Retain only trusted wire-relevant metadata for exact peer comparison.
		state.wire[id] = cloneRelayRegion(region)
		pinned := cloneRelayRegion(region)
		for _, node := range pinned.Nodes {
			nodes++
			if node == nil || nodes > maxRelayNodes || node.RegionID != id || len(node.Name) == 0 || len(node.Name) > 64 || names[node.Name] || !relayHost(node.HostName) || (node.CertName != "" && !relayHost(node.CertName)) || node.InsecureForTests || node.STUNTestIP != "" || node.DERPPort < 0 || node.DERPPort > 65535 || node.STUNPort < -1 || node.STUNPort > 65535 {
				return nil, ErrRelayProfile
			}
			names[node.Name] = true
			var ips []netip.Addr
			for _, encoded := range []string{node.IPv4, node.IPv6} {
				if encoded == "" || encoded == "none" {
					continue
				}
				ip, err := netip.ParseAddr(encoded)
				if err != nil || !publicRelayIP(ip) || (encoded == node.IPv4 && !ip.Is4()) || (encoded == node.IPv6 && !ip.Is6()) {
					return nil, ErrRelayProfile
				}
				ips = append(ips, ip)
			}
			if node.IPv4 == "" || node.IPv6 == "" {
				resolved, err := resolve(ctx, node.HostName)
				if err != nil {
					return nil, ErrRelayProfile
				}
				for _, ip := range resolved {
					if !publicRelayIP(ip) {
						return nil, ErrRelayProfile
					}
					if ip.Is4() && node.IPv4 == "" || ip.Is6() && node.IPv6 == "" {
						ips = append(ips, ip)
					}
				}
			}
			node.IPv4, node.IPv6 = "none", "none"
			for _, ip := range ips {
				if ip.Is4() && node.IPv4 == "none" {
					node.IPv4 = ip.String()
				} else if ip.Is6() && node.IPv6 == "none" {
					node.IPv6 = ip.String()
				}
			}
			if node.IPv4 == "none" && node.IPv6 == "none" {
				return nil, ErrRelayProfile
			}
			node.CanPort80 = false
		}
		state.regions[id] = pinned
		if !region.Avoid && !region.NoMeasureNoHome {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || ctx.Err() != nil {
		return nil, ErrRelayProfile
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	state.defaultRegion = ids[0]
	return &RelayProfile{state: state}, nil
}

// trustedEndpoint replaces peer metadata with the selected operator's pinned
// region. Matching an ID does not authorize a peer's altered hostname or port.
func (p *RelayProfile) trustedEndpoint(endpoint address.Endpoint) (tailcat.Addr, error) {
	if !p.Ready() || !endpoint.Valid() {
		return "", ErrRelayProfile
	}
	ci, err := tailcat.ParseAddr(endpoint.EncodeForTransport())
	if err != nil {
		return "", ErrRelayProfile
	}
	id := ci.RegionID
	if len(ci.Region) != 0 {
		if len(ci.Region) != 1 || ci.Region[0] == nil {
			return "", ErrRelayProfile
		}
		// Embedded addresses elide the real region ID; ParseAddr synthesizes 1.
		// Compare the complete canonical wire subset against approved regions,
		// rather than trusting that synthetic ID or any peer-provided host.
		id = 0
		for approvedID, trusted := range p.state.wire {
			candidate := ci
			candidate.Region = []*tailcfg.DERPRegion{cloneRelayRegion(trusted)}
			candidate.RegionID = 0
			if candidate.Addr() == endpoint.EncodeForTransport() && (id == 0 || approvedID < id) {
				id = approvedID
			}
		}
		if id == 0 {
			return "", ErrRelayProfile
		}
	}
	region := p.state.regions[id]
	if region == nil {
		return "", ErrRelayProfile
	}
	ci.RegionID = 0
	ci.Region = []*tailcfg.DERPRegion{cloneRelayRegion(region)}
	return ci.Addr(), nil
}
