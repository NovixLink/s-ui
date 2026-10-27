//go:build !race

package service

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"testing"

	"github.com/alireza0/s-ui/core"
	"github.com/alireza0/s-ui/database/model"
)

// The real Box has an upstream NetworkManager startup race. The shared pool's
// concurrent network lifecycle is exercised separately in core/protocol/vless.
func TestRealityTLSBatchSavePreservesLiveGroup(t *testing.T) {
	for _, change := range []string{"invalid-key", "disable-reality", "disable-tls", "valid-edit"} {
		t.Run(change, func(t *testing.T) {
			db := clientTestDB(t)
			previousCore := corePtr
			c := core.NewCore()
			cfgService := NewConfigService(c)
			t.Cleanup(func() { _ = c.Stop(); corePtr = previousCore })
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
			original := fmt.Sprintf(`{"enabled":true,"server_name":"relay.test","reality":{"enabled":true,"handshake":{"server":"127.0.0.1","server_port":1},"private_key":%q,"short_id":["aa"]}}`, private)
			tls := model.Tls{Name: "review-shared", Server: json.RawMessage(original), Client: json.RawMessage(fmt.Sprintf(`{"enabled":true,"server_name":"relay.test","reality":{"enabled":true,"public_key":%q}}`, public))}
			if err = db.Create(&tls).Error; err != nil {
				t.Fatal(err)
			}
			configs := make([]json.RawMessage, 0, 2)
			for _, tag := range []string{"review-a", "review-b"} {
				in := model.Inbound{Type: "vless", Tag: tag, TlsId: tls.Id, Tls: &tls, Addrs: json.RawMessage(`[]`), OutJson: json.RawMessage(`{}`), Options: json.RawMessage(fmt.Sprintf(`{"listen":"127.0.0.1","listen_port":%d}`, port))}
				if err = db.Create(&in).Error; err != nil {
					t.Fatal(err)
				}
				raw, err := in.MarshalJSON()
				if err != nil {
					t.Fatal(err)
				}
				configs = append(configs, raw)
			}
			cfg, _ := json.Marshal(map[string]interface{}{"log": map[string]string{"level": "error"}, "inbounds": configs, "outbounds": []interface{}{map[string]string{"type": "direct", "tag": "direct"}}})
			if err = c.Start(cfg); err != nil {
				t.Fatal(err)
			}
			beforeInbounds := c.GetInstance().Inbound().Inbounds()
			before := len(beforeInbounds)
			var updated map[string]interface{}
			_ = json.Unmarshal(tls.Server, &updated)
			reality := updated["reality"].(map[string]interface{})
			if change == "invalid-key" {
				reality["private_key"] = "invalid-key"
			} else if change == "disable-reality" {
				reality["enabled"] = false
			} else if change == "disable-tls" {
				updated["enabled"] = false
			} else {
				updated["handshake_timeout"] = "10s"
			}
			tls.Server, _ = json.Marshal(updated)
			payload, _ := json.Marshal(tls)
			_, saveErr := cfgService.Save("tls", "edit", payload, "", "review-admin", "relay.test")
			if change != "valid-edit" && saveErr == nil {
				t.Fatal("fixture expected rejected edit")
			}
			if change == "valid-edit" && saveErr != nil {
				t.Fatal(saveErr)
			}
			var stored model.Tls
			if err = db.First(&stored, tls.Id).Error; err != nil {
				t.Fatal(err)
			}
			after := len(c.GetInstance().Inbound().Inbounds())
			t.Logf("change=%s save_error=%v live_before=%d live_after=%d database_rolled_back=%v core_reports_running=%v", change, saveErr, before, after, string(stored.Server) == original, c.IsRunning())
			if after != before {
				t.Fatalf("edit removed %d of %d live inbounds", before-after, before)
			}
			if change != "valid-edit" {
				if string(stored.Server) != original {
					t.Fatal("rejected edit changed the persisted TLS template")
				}
				for _, old := range beforeInbounds {
					current, ok := c.GetInstance().Inbound().Get(old.Tag())
					if !ok || current != old {
						t.Fatalf("rejected edit replaced live inbound %s", old.Tag())
					}
				}
			} else if string(stored.Server) == original {
				t.Fatal("valid edit was not persisted")
			}
		})
	}
}
