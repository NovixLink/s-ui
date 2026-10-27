package vless

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"sync"

	utls "github.com/metacubex/utls"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/option"
	N "github.com/sagernet/sing/common/network"
)

type realityShortIDReader interface {
	RealityShortID() ([8]byte, bool)
}

// Building this fork requires the accessor patch, not just the upstream module.
var _ realityShortIDReader = (*utls.Conn)(nil)

// SharedListeners belongs to one Box. Logical inbounds retain their own VLESS
// authentication, mux, sessions and tag; only the TCP/REALITY entry is shared.
type SharedListeners struct {
	mu     sync.RWMutex
	groups map[string]*sharedListener
}

type SharedInbound struct {
	Tag     string
	Options option.VLESSInboundOptions
}

type sharedListener struct {
	pool      *SharedListeners
	profile   string
	listener  *listener.Listener
	tls       tls.ServerConfig
	members   map[string]*Inbound
	selectors map[[8]byte]*Inbound
}

func NewSharedListeners() *SharedListeners {
	return &SharedListeners{groups: make(map[string]*sharedListener)}
}

func sharedEnabled(o option.VLESSInboundOptions) bool {
	return o.ListenPort != 0 && o.TLS != nil && o.TLS.Enabled &&
		o.TLS.Reality != nil && o.TLS.Reality.Enabled &&
		(o.Transport == nil || o.Transport.Type == "")
}

func sharedProfile(o option.VLESSInboundOptions) (string, string, error) {
	// Match listener.ListenTCP's default address. Address, port and namespace
	// are already owned by the group key, not duplicated in its TLS profile.
	key := sharedAddress(o)
	listen := o.ListenOptions
	listen.Listen = nil
	listen.ListenPort = 0
	listen.NetNs = ""
	server := *o.TLS
	reality := *server.Reality
	reality.ShortID = nil
	server.Reality = &reality
	profile, err := json.Marshal(struct {
		Listen option.ListenOptions
		TLS    option.InboundTLSOptions
	}{listen, server})
	return key, string(profile), err
}

func sharedAddress(o option.VLESSInboundOptions) string {
	return fmt.Sprint(o.Listen.Build(netip.AddrFrom4([4]byte{127, 0, 0, 1})), ":", o.ListenPort, ":", o.NetNs)
}

func shortIDs(o option.VLESSInboundOptions) ([][8]byte, error) {
	var result [][8]byte
	for _, value := range o.TLS.Reality.ShortID {
		if len(value) == 0 || len(value) > 16 || len(value)%2 != 0 {
			return nil, fmt.Errorf("shared REALITY requires a short ID of 1 to 8 bytes")
		}
		var id [8]byte
		n, err := hex.Decode(id[:], []byte(value))
		if err != nil || n == 0 || n > len(id) {
			return nil, fmt.Errorf("shared REALITY requires a non-empty short ID of at most 8 bytes")
		}
		result = append(result, id)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("shared REALITY requires a fixed short ID for each inbound")
	}
	return result, nil
}

// Validate checks the final batch before a panel edit removes live inbounds.
// This also permits changing the common TLS template for a whole group at once.
func (p *SharedListeners) Validate(add []SharedInbound, remove []string) error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	removed := make(map[string]bool, len(remove))
	for _, tag := range remove {
		removed[tag] = true
	}
	planned := make(map[string][]SharedInbound)
	for key, group := range p.groups {
		for tag, in := range group.members {
			if !removed[tag] {
				planned[key] = append(planned[key], SharedInbound{tag, in.sharedOptions})
			}
		}
	}
	for _, in := range add {
		if in.Options.ListenPort == 0 {
			continue
		}
		key := sharedAddress(in.Options)
		planned[key] = append(planned[key], in)
	}
	for _, entries := range planned {
		profile := ""
		seen := make(map[[8]byte]string)
		for _, in := range entries {
			if !sharedEnabled(in.Options) {
				if len(entries) > 1 {
					return fmt.Errorf("only raw TCP VLESS with REALITY can share a listen address and port; use a separate port for inbound %q", in.Tag)
				}
				continue
			}
			_, current, err := sharedProfile(in.Options)
			if err != nil {
				return err
			}
			if profile != "" && profile != current {
				return fmt.Errorf("shared VLESS REALITY inbounds must use one SNI and identical TLS/listen settings")
			}
			profile = current
			ids, err := shortIDs(in.Options)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if owner, ok := seen[id]; ok && owner != in.Tag {
					return fmt.Errorf("REALITY short ID is already assigned to inbound %q", owner)
				}
				seen[id] = in.Tag
			}
		}
	}
	return nil
}

func (p *SharedListeners) add(in *Inbound) error {
	o := in.sharedOptions
	key, profile, err := sharedProfile(o)
	if err != nil {
		return err
	}
	ids, err := shortIDs(o)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	g := p.groups[key]
	if g == nil {
		g = &sharedListener{pool: p, profile: profile,
			members: make(map[string]*Inbound), selectors: make(map[[8]byte]*Inbound)}
	} else if g.profile != profile {
		return fmt.Errorf("shared VLESS REALITY inbounds must use one SNI and identical TLS/listen settings")
	}
	for _, id := range ids {
		if owner := g.selectors[id]; owner != nil && owner != in {
			return fmt.Errorf("REALITY short ID is already assigned to inbound %q", owner.Tag())
		}
	}
	server := *o.TLS
	reality := *server.Reality
	values := make(map[string]bool)
	for id := range g.selectors {
		values[hex.EncodeToString(id[:])] = true
	}
	for _, id := range ids {
		values[hex.EncodeToString(id[:])] = true
	}
	reality.ShortID = nil
	for value := range values {
		reality.ShortID = append(reality.ShortID, value)
	}
	sort.Strings(reality.ShortID)
	server.Reality = &reality
	config, err := tls.NewServerWithOptions(tls.ServerOptions{Context: in.ctx, Logger: in.logger, Options: server})
	if err != nil {
		return err
	}
	if err = config.Start(); err != nil {
		return err
	}
	if g.listener == nil {
		g.listener = listener.New(listener.Options{Context: in.ctx, Logger: in.logger,
			Network: []string{N.NetworkTCP}, Listen: o.ListenOptions, ConnectionHandler: g})
		if err = g.listener.Start(); err != nil {
			_ = config.Close()
			return err
		}
	}
	previous := g.tls
	g.tls = config
	g.members[in.Tag()] = in
	for _, id := range ids {
		g.selectors[id] = in
	}
	p.groups[key] = g
	if previous != nil {
		_ = previous.Close()
	}
	return nil
}

func (p *SharedListeners) remove(in *Inbound) error {
	key, _, _ := sharedProfile(in.sharedOptions)
	p.mu.Lock()
	defer p.mu.Unlock()
	g := p.groups[key]
	if g == nil || g.members[in.Tag()] != in {
		return nil
	}
	delete(g.members, in.Tag())
	for id, member := range g.selectors {
		if member == in {
			delete(g.selectors, id)
		}
	}
	if len(g.members) != 0 {
		return nil
	}
	delete(p.groups, key)
	err := g.listener.Close()
	_ = g.tls.Close()
	return err
}

func (g *sharedListener) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	g.pool.mu.RLock()
	config := g.tls
	g.pool.mu.RUnlock()
	tlsConn, err := tls.ServerHandshake(ctx, conn, config)
	if err != nil {
		N.CloseOnHandshakeFailure(conn, onClose, err)
		return
	}
	id, ok := authenticatedShortID(tlsConn)
	g.pool.mu.RLock()
	in := g.selectors[id]
	g.pool.mu.RUnlock()
	if !ok || in == nil {
		N.CloseOnHandshakeFailure(tlsConn, onClose, fmt.Errorf("unknown REALITY inbound selector"))
		return
	}
	// The selected inbound authenticates against its original user list and
	// writes its original tag into metadata, preserving route and quota behavior.
	in.acceptVLESS(ctx, tlsConn, metadata, onClose)
}

func authenticatedShortID(conn net.Conn) ([8]byte, bool) {
	for {
		if reader, ok := conn.(realityShortIDReader); ok {
			return reader.RealityShortID()
		}
		wrapper, ok := conn.(interface{ Upstream() any })
		if !ok {
			return [8]byte{}, false
		}
		conn, ok = wrapper.Upstream().(net.Conn)
		if !ok {
			return [8]byte{}, false
		}
	}
}
