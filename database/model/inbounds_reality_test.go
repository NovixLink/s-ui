package model

import (
	"encoding/json"
	"testing"
)

func TestRealityShortIDFollowsRecord(t *testing.T) {
	i := Inbound{Id: 14, Type: "vless", Tag: "line-a", Tls: &Tls{
		Server: json.RawMessage(`{"enabled":true,"reality":{"enabled":true,"short_id":["aaaa","bbbb"]}}`),
	}}
	selector := func(in Inbound) string {
		t.Helper()
		var tls map[string]interface{}
		if err := json.Unmarshal(in.Tls.Server, &tls); err != nil {
			t.Fatal(err)
		}
		in.ApplyRealityShortID(tls, false)
		return tls["reality"].(map[string]interface{})["short_id"].(string)
	}
	want := selector(i)
	if want != "000000000000000e" {
		t.Fatalf("selector = %q", want)
	}
	i.Tag = "renamed-line"
	if got := selector(i); got != want {
		t.Fatalf("rename changed selector: %q", got)
	}
	i.Id++
	if got := selector(i); got == want {
		t.Fatal("a recreated record reused the selector")
	}
	i.Id = 14
	raw, err := i.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var server struct {
		TLS struct {
			Reality struct {
				ShortID []string `json:"short_id"`
			} `json:"reality"`
		} `json:"tls"`
	}
	if err = json.Unmarshal(raw, &server); err != nil {
		t.Fatal(err)
	}
	if len(server.TLS.Reality.ShortID) != 1 || server.TLS.Reality.ShortID[0] != want {
		t.Fatalf("server and subscription selectors differ: %s", raw)
	}
	if string(i.Tls.Server) != `{"enabled":true,"reality":{"enabled":true,"short_id":["aaaa","bbbb"]}}` {
		t.Fatal("modified the shared TLS template")
	}
}

func TestRealitySelectorDoesNotChangeOtherProtocols(t *testing.T) {
	for _, protocol := range []string{"trojan", "vmess", "shadowsocks"} {
		in := Inbound{Id: 1, Type: protocol}
		tls := map[string]interface{}{"reality": map[string]interface{}{"enabled": true, "short_id": "original"}}
		in.ApplyRealityShortID(tls, false)
		if tls["reality"].(map[string]interface{})["short_id"] != "original" {
			t.Fatal(protocol)
		}
	}
	in := Inbound{Id: 1, Type: "vless"}
	tls := map[string]interface{}{"enabled": true, "server_name": "ordinary-tls.test"}
	in.ApplyRealityShortID(tls, false)
	if len(tls) != 2 {
		t.Fatal("added REALITY to ordinary TLS")
	}
}
