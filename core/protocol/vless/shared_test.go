//go:build with_utls

package vless

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	stdtls "crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	protocol "github.com/sagernet/sing-vmess/vless"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
)

type tagEchoRouter struct{ adapter.Router }

func (r *tagEchoRouter) RouteConnectionEx(_ context.Context, conn net.Conn, metadata adapter.InboundContext, _ N.CloseHandlerFunc) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	var request [1]byte
	if _, err := io.ReadFull(conn, request[:]); err == nil && metadata.User == "alice" {
		_, _ = conn.Write([]byte(metadata.Inbound))
	}
}

func sharedRealityTarget(t *testing.T) uint16 {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"relay.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	l, err := stdtls.Listen("tcp", "127.0.0.1:0", &stdtls.Config{
		Certificates: []stdtls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   stdtls.VersionTLS13, NextProtos: []string{"h2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}

// Exercises the real listener, REALITY handshake and VLESS authentication
// without starting Box's unrelated NetworkManager, so this also runs with -race.
func TestSharedListenersConcurrentHandshakes(t *testing.T) {
	target := sharedRealityTarget(t)
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private := base64.RawURLEncoding.EncodeToString(key.Bytes())
	public := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	pool := NewSharedListeners()
	ctx := service.ContextWithDefaultRegistry(context.Background())
	ctx = service.ContextWith[*SharedListeners](ctx, pool)
	logger := log.NewNOPFactory().Logger()
	const uuid = "bf000d23-0752-40b4-affe-68f7707a9661"
	newInbound := func(tag, sid string) *Inbound {
		t.Helper()
		var options option.VLESSInboundOptions
		raw := fmt.Sprintf(`{"listen":"127.0.0.1","listen_port":%d,"users":[{"name":"alice","uuid":%q}],"tls":{"enabled":true,"server_name":"relay.test","reality":{"enabled":true,"handshake":{"server":"127.0.0.1","server_port":%d},"private_key":%q,"short_id":[%q]}}}`, port, uuid, target, private, sid)
		if err := json.Unmarshal([]byte(raw), &options); err != nil {
			t.Fatal(err)
		}
		in, err := NewInbound(ctx, &tagEchoRouter{}, logger, tag, options)
		if err != nil {
			t.Fatal(err)
		}
		return in.(*Inbound)
	}
	a := newInbound("line-a", "0000000000000001")
	b := newInbound("line-b", "0000000000000002")
	for _, in := range []*Inbound{a, b} {
		if err := pool.add(in); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pool.remove(in) })
	}
	probe := func(sid, want string) error {
		var options option.OutboundTLSOptions
		raw := fmt.Sprintf(`{"enabled":true,"server_name":"relay.test","utls":{"enabled":true,"fingerprint":"chrome"},"reality":{"enabled":true,"public_key":%q,"short_id":%q}}`, public, sid)
		if err := json.Unmarshal([]byte(raw), &options); err != nil {
			return err
		}
		config, err := tls.NewClient(ctx, logger, "127.0.0.1", options)
		if err != nil {
			return err
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprint("127.0.0.1:", port), 5*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		secured, err := tls.ClientHandshake(ctx, conn, config)
		if err != nil {
			return err
		}
		client, err := protocol.NewClient(uuid, "", logger)
		if err != nil {
			return err
		}
		stream, err := client.DialConn(secured, M.ParseSocksaddr("127.0.0.1:1"))
		if err != nil {
			return err
		}
		if _, err = stream.Write([]byte("x")); err != nil {
			return err
		}
		reply := make([]byte, len(want))
		if _, err = io.ReadFull(stream, reply); err != nil {
			return err
		}
		if string(reply) != want {
			return fmt.Errorf("routed to %q, want %q", reply, want)
		}
		return nil
	}
	stop := make(chan struct{})
	mutatorDone := make(chan error, 1)
	var changes atomic.Int32
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				mutatorDone <- nil
				return
			case <-ticker.C:
				if err := pool.remove(b); err != nil {
					mutatorDone <- err
					return
				}
				if err := pool.add(b); err != nil {
					mutatorDone <- err
					return
				}
				changes.Add(1)
			}
		}
	}()
	var clients sync.WaitGroup
	for range 4 {
		clients.Add(1)
		go func() {
			defer clients.Done()
			for range 20 {
				if err := probe("0000000000000001", "line-a"); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	clients.Wait()
	close(stop)
	if err := <-mutatorDone; err != nil {
		t.Fatal(err)
	}
	if changes.Load() == 0 {
		t.Fatal("no concurrent membership changes exercised")
	}
	if err := pool.remove(a); err != nil {
		t.Fatal(err)
	}
	if err := probe("0000000000000002", "line-b"); err != nil {
		t.Fatal("removing the first member interrupted its peer:", err)
	}
	if err := pool.remove(b); err != nil {
		t.Fatal(err)
	}
	rebound, err := net.Listen("tcp", fmt.Sprint("127.0.0.1:", port))
	if err != nil {
		t.Fatal("last removal did not release the listener:", err)
	}
	_ = rebound.Close()
}
