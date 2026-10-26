package core

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	stdtls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

func realityTarget(t *testing.T) int {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "relay.test"},
		DNSNames: []string{"relay.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
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
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = io.Copy(io.Discard, c)
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func markerBackend(t *testing.T, marker string) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	u, err := net.ListenPacket("udp", fmt.Sprint("127.0.0.1:", port))
	if err != nil {
		l.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close(); _ = u.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				b := make([]byte, 1)
				if _, err := io.ReadFull(c, b); err == nil {
					_, _ = c.Write([]byte(marker))
				}
			}()
		}
	}()
	go func() {
		b := make([]byte, 1024)
		for {
			_, a, err := u.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = u.WriteTo([]byte(marker), a)
		}
	}()
	return port
}

func TestSharedRealityRoutingAndLifecycle(t *testing.T) {
	if raceEnabled {
		t.Skip("upstream route.NetworkManager races during Box startup; shared pool has separate race tests")
	}
	target := realityTarget(t)
	a, b := markerBackend(t, "A"), markerBackend(t, "B")
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private := base64.RawURLEncoding.EncodeToString(key.Bytes())
	public := base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	port := freePort(t)
	uuid := "bf000d23-0752-40b4-affe-68f7707a9661"
	makeInbound := func(tag, sid, serverName, flow string) string {
		return fmt.Sprintf(`{"type":"vless","tag":%q,"listen":"127.0.0.1","listen_port":%d,"users":[{"name":"alice","uuid":%q,"flow":%q}],"tls":{"enabled":true,"server_name":%q,"reality":{"enabled":true,"handshake":{"server":"127.0.0.1","server_port":%d},"private_key":%q,"short_id":[%q]}}}`, tag, port, uuid, flow, serverName, target, private, sid)
	}
	makeOutbound := func(tag, sid, flow string) string {
		return fmt.Sprintf(`{"type":"vless","tag":%q,"server":"127.0.0.1","server_port":%d,"uuid":%q,"flow":%q,"tls":{"enabled":true,"server_name":"relay.test","utls":{"enabled":true,"fingerprint":"chrome"},"reality":{"enabled":true,"public_key":%q,"short_id":%q}}}`, tag, port, uuid, flow, public, sid)
	}
	for _, flow := range []string{"", "xtls-rprx-vision"} {
		t.Run("flow="+flow, func(t *testing.T) {
			server := NewCore()
			t.Cleanup(func() { _ = server.Stop() })
			ia, ib := makeInbound("line-a", "0000000000000001", "relay.test", flow), makeInbound("line-b", "0000000000000002", "relay.test", flow)
			if flow == "" {
				// An omitted address means loopback in sing-box, so it must share
				// the same listener as an explicitly configured loopback address.
				ib = strings.Replace(ib, `"listen":"127.0.0.1",`, "", 1)
			}
			cfg := fmt.Sprintf(`{"log":{"level":"error"},"inbounds":[%s,%s],"outbounds":[{"type":"direct","tag":"direct"}],"route":{"rules":[{"inbound":["line-a"],"action":"route","outbound":"direct","override_address":"127.0.0.1","override_port":%d},{"inbound":["line-b"],"action":"route","outbound":"direct","override_address":"127.0.0.1","override_port":%d}]}}`, ia, ib, a, b)
			if err := server.Start([]byte(cfg)); err != nil {
				t.Fatal(err)
			}
			client := NewCore()
			t.Cleanup(func() { _ = client.Stop() })
			if err := client.Start([]byte(fmt.Sprintf(`{"log":{"level":"error"},"outbounds":[%s,%s]}`, makeOutbound("a", "0000000000000001", flow), makeOutbound("b", "0000000000000002", flow)))); err != nil {
				t.Fatal(err)
			}
			probe := func(tag string, want byte, udp bool) error {
				out, _ := client.GetInstance().outbound.Outbound(tag)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				destination := M.ParseSocksaddr("127.0.0.1:1")
				var reply [2048]byte
				if udp {
					c, err := out.ListenPacket(ctx, destination)
					if err != nil {
						return err
					}
					defer c.Close()
					_ = c.SetDeadline(time.Now().Add(5 * time.Second))
					if _, err = c.WriteTo([]byte("x"), destination.UDPAddr()); err != nil {
						return err
					}
					if _, _, err = c.ReadFrom(reply[:]); err != nil {
						return err
					}
				} else {
					c, err := out.DialContext(ctx, "tcp", destination)
					if err != nil {
						return err
					}
					defer c.Close()
					_ = c.SetDeadline(time.Now().Add(5 * time.Second))
					if _, err = c.Write([]byte("x")); err != nil {
						return err
					}
					if _, err = io.ReadFull(c, reply[:1]); err != nil {
						return err
					}
				}
				if reply[0] != want {
					return fmt.Errorf("%s routed to %q, want %q", tag, reply[0], want)
				}
				return nil
			}
			for _, udp := range []bool{false, true} {
				for _, line := range []struct {
					tag    string
					marker byte
				}{{"a", 'A'}, {"b", 'B'}} {
					if err := probe(line.tag, line.marker, udp); err != nil {
						t.Fatalf("udp=%v: %v", udp, err)
					}
				}
			}
			seen := make(map[string]bool)
			for _, stat := range *server.GetInstance().SessionTracker().GetStats() {
				seen[stat.Resource+":"+stat.Tag] = true
				if stat.Resource == "user" && stat.Tag != "alice" {
					t.Fatalf("changed user attribution: %q", stat.Tag)
				}
			}
			for _, key := range []string{"inbound:line-a", "inbound:line-b", "user:alice"} {
				if !seen[key] {
					t.Fatalf("missing original traffic attribution: %s", key)
				}
			}
			bad := makeInbound("line-b", "0000000000000002", "another-sni.test", flow)
			if err := server.CheckInbounds([][]byte{[]byte(bad)}, []string{"line-b"}); err == nil || !strings.Contains(err.Error(), "one SNI") {
				t.Fatalf("SNI mismatch: %v", err)
			}
			if err := probe("a", 'A', false); err != nil {
				t.Fatal("rejected edit interrupted A:", err)
			}
			if err := probe("b", 'B', false); err != nil {
				t.Fatal("rejected edit interrupted B:", err)
			}
			duplicate := makeInbound("line-c", "0000000000000001", "relay.test", flow)
			if err := server.AddInbound([]byte(duplicate)); err == nil {
				t.Fatal("accepted a selector assigned to another inbound")
			}
			// Removing one logical inbound keeps the shared socket and its peer alive.
			if err := server.RemoveInbound("line-b"); err != nil {
				t.Fatal(err)
			}
			if err := probe("a", 'A', false); err != nil {
				t.Fatal(err)
			}
			if err := probe("b", 'B', false); err == nil {
				t.Fatal("deleted selector remained usable")
			}
			if err := server.AddInbound([]byte(ib)); err != nil {
				t.Fatal(err)
			}
			if err := probe("b", 'B', false); err != nil {
				t.Fatal(err)
			}
			// The first member originally created the socket. Its removal must
			// not cancel the listener while another logical inbound still uses it.
			if err := server.RemoveInbound("line-a"); err != nil {
				t.Fatal(err)
			}
			if err := probe("b", 'B', false); err != nil {
				t.Fatal("first-member removal interrupted B:", err)
			}
			if err := server.AddInbound([]byte(ia)); err != nil {
				t.Fatal(err)
			}
			if err := probe("a", 'A', false); err != nil {
				t.Fatal(err)
			}
			// Selecting a line never bypasses that line's original UUID membership.
			var noUsers map[string]interface{}
			_ = json.Unmarshal([]byte(ib), &noUsers)
			noUsers["users"] = []interface{}{}
			raw, _ := json.Marshal(noUsers)
			if handled, err := server.UpdateInboundUsers(raw); err != nil || !handled {
				t.Fatalf("update users: %v %v", handled, err)
			}
			if err := probe("b", 'B', false); err == nil {
				t.Fatal("selector bypassed VLESS membership")
			}
			if err := probe("a", 'A', false); err != nil {
				t.Fatal(err)
			}
			if err := server.Stop(); err != nil {
				t.Fatal(err)
			}
			if portBound(port) {
				t.Fatal("shared socket leaked after Stop")
			}
			if err := server.Start([]byte(cfg)); err != nil {
				t.Fatal(err)
			}
			for _, line := range []struct {
				tag    string
				marker byte
			}{{"a", 'A'}, {"b", 'B'}} {
				if err := probe(line.tag, line.marker, false); err != nil {
					t.Fatal("restart rotated or lost selector:", err)
				}
			}
		})
	}
}
