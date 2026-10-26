package sub

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/alireza0/s-ui/database"
	"github.com/alireza0/s-ui/database/model"
	"github.com/alireza0/s-ui/util"
	"gopkg.in/yaml.v3"
)

func TestRealitySelectorsAgreeAcrossSubscriptions(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "test.db")); err != nil {
		t.Fatal(err)
	}
	client := json.RawMessage(`{"vless":{"name":"alice","uuid":"bf000d23-0752-40b4-affe-68f7707a9661"}}`)
	tls := &model.Tls{Id: 1,
		Server: json.RawMessage(`{"enabled":true,"server_name":"relay.test","reality":{"enabled":true,"short_id":["aaaa","bbbb"]}}`),
		Client: json.RawMessage(`{"reality":{"enabled":true,"public_key":"test-public-key"}}`),
	}
	var inbounds []*model.Inbound
	for id, tag := range []string{"line-a", "line-b"} {
		in := &model.Inbound{Id: uint(id + 1), Type: "vless", Tag: tag, TlsId: 1, Tls: tls, Options: json.RawMessage(`{"listen_port":443}`), Addrs: json.RawMessage(`[]`), OutJson: json.RawMessage(`{}`)}
		if id == 1 {
			in.Addrs = json.RawMessage(`[{"server":"alternate.example","server_port":443,"tls":{"reality":{"enabled":true,"short_id":"ffff","public_key":"test-public-key"}}}]`)
		}
		if err := util.FillOutJson(in, "relay.example"); err != nil {
			t.Fatal(err)
		}
		inbounds = append(inbounds, in)
	}
	for attempt := 0; attempt < 5; attempt++ {
		outbounds, _, err := (&JsonService{}).getOutbounds(client, inbounds, "")
		if err != nil {
			t.Fatal(err)
		}
		clash, err := (&ClashService{}).ConvertToClashMeta(outbounds, basicClashConfig)
		if err != nil {
			t.Fatal(err)
		}
		var yamlConfig struct {
			Proxies []map[string]interface{} `yaml:"proxies"`
		}
		if err = yaml.Unmarshal([]byte(clash), &yamlConfig); err != nil {
			t.Fatal(err)
		}
		for idx, in := range inbounds {
			want := []string{"0000000000000001", "0000000000000002"}[idx]
			links := util.LinkGenerator(client, in, "relay.example", "")
			if len(links) != 1 {
				t.Fatalf("links: %v", links)
			}
			uri, err := url.Parse(links[0])
			if err != nil {
				t.Fatal(err)
			}
			if uri.Query().Get("sid") != want {
				t.Fatalf("link SID: %s", links[0])
			}
			out := (*outbounds)[idx]
			if out["tls"].(map[string]interface{})["reality"].(map[string]interface{})["short_id"] != want {
				t.Fatalf("JSON SID: %v", out)
			}
			if out["uuid"] != "bf000d23-0752-40b4-affe-68f7707a9661" || out["tag"] != in.Tag {
				t.Fatal("changed UUID or tag")
			}
			if yamlConfig.Proxies[idx]["reality-opts"].(map[string]interface{})["short-id"] != want {
				t.Fatalf("Clash SID: %v", yamlConfig.Proxies[idx])
			}
		}
	}
}
