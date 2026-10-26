package service

import (
	"encoding/json"
	"fmt"
	"net/url"
	"testing"

	"github.com/alireza0/s-ui/core"
	"github.com/alireza0/s-ui/database/model"
	"gorm.io/gorm"
)

func TestRealityBackfillPreservesExistingBehavior(t *testing.T) {
	db := clientTestDB(t)
	tls := model.Tls{Name: "shared", Server: json.RawMessage(`{"enabled":true,"server_name":"relay.test","reality":{"enabled":true,"short_id":["aaaa","bbbb"]}}`), Client: json.RawMessage(`{}`)}
	if err := db.Create(&tls).Error; err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{Type: "vless", Tag: "existing-tag", TlsId: tls.Id, Options: json.RawMessage(`{"listen":"::","listen_port":38859}`), OutJson: json.RawMessage(`{"type":"vless","server":"relay.example","server_port":38859,"tls":{"enabled":true,"reality":{"enabled":true,"short_id":"aaaa"}}}`)}
	if err := db.Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	uri := "vless://bf000d23-0752-40b4-affe-68f7707a9661@relay.example:38859?security=reality&sni=relay.test&sid=bbbb#custom-name"
	links, _ := json.Marshal([]map[string]string{{"type": "local", "remark": in.Tag, "uri": uri}, {"type": "external", "remark": in.Tag, "uri": uri}})
	c := createClient(t, db, &model.Client{Name: "alice", Config: json.RawMessage(`{"vless":{"uuid":"original-uuid"}}`), Inbounds: json.RawMessage(fmt.Sprintf(`[%d]`, in.Id)), Links: links, Up: 100, Down: 200, Volume: 1000})
	base := &model.Setting{Key: "config", Value: `{"route":{"rules":[{"inbound":["existing-tag"],"action":"route","outbound":"existing-exit"}]}}`}
	if err := db.Create(base).Error; err != nil {
		t.Fatal(err)
	}
	s := &InboundService{}
	if err := s.SyncRealityShortIDs(); err != nil {
		t.Fatal(err)
	}
	got := reload(t, db, c.Id)
	if got.Up != 100 || got.Down != 200 || got.Volume != 1000 || string(got.Config) != string(c.Config) || string(got.Inbounds) != string(c.Inbounds) {
		t.Fatal("changed user, membership or quota")
	}
	var updated []map[string]string
	_ = json.Unmarshal(got.Links, &updated)
	parsed, err := url.Parse(updated[0]["uri"])
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%016x", in.Id)
	if parsed.Query().Get("sid") != want || parsed.Host != "relay.example:38859" || parsed.User.Username() != "bf000d23-0752-40b4-affe-68f7707a9661" || parsed.Fragment != "custom-name" {
		t.Fatal(updated[0])
	}
	if updated[1]["uri"] != uri {
		t.Fatal("changed an external link")
	}
	var current model.Setting
	_ = db.First(&current, base.Id).Error
	if current.Value != base.Value {
		t.Fatal("changed route rules")
	}
	before := string(got.Links)
	if err := s.SyncRealityShortIDs(); err != nil {
		t.Fatal(err)
	}
	if after := string(reload(t, db, c.Id).Links); after != before {
		t.Fatal("second startup rewrote stable links")
	}
}

func TestNewRealityInboundGetsPermanentSelector(t *testing.T) {
	db := clientTestDB(t)
	previousCore := corePtr
	t.Cleanup(func() { corePtr = previousCore })
	NewConfigService(core.NewCore())
	tls := model.Tls{Name: "shared", Server: json.RawMessage(`{"enabled":true,"server_name":"relay.test","reality":{"enabled":true,"short_id":["aaaa"]}}`), Client: json.RawMessage(`{"reality":{"enabled":true,"public_key":"test-public-key"}}`)}
	if err := db.Create(&tls).Error; err != nil {
		t.Fatal(err)
	}
	create := func(tag string) model.Inbound {
		t.Helper()
		data := json.RawMessage(fmt.Sprintf(`{"id":99,"type":"vless","tag":%q,"tls_id":%d,"listen":"::","listen_port":443,"addrs":[]}`, tag, tls.Id))
		if err := db.Transaction(func(tx *gorm.DB) error { return (&InboundService{}).Save(tx, "new", data, "", "relay.example") }); err != nil {
			t.Fatal(err)
		}
		var in model.Inbound
		if err := db.Where("tag = ?", tag).First(&in).Error; err != nil {
			t.Fatal(err)
		}
		var out struct {
			TLS struct {
				Reality struct {
					ShortID string `json:"short_id"`
				} `json:"reality"`
			} `json:"tls"`
		}
		if err := json.Unmarshal(in.OutJson, &out); err != nil {
			t.Fatal(err)
		}
		if in.Id == 0 || in.Id == 99 || out.TLS.Reality.ShortID != fmt.Sprintf("%016x", in.Id) {
			t.Fatalf("new inbound: %+v %s", in, in.OutJson)
		}
		return in
	}
	first := create("first")
	if err := db.Delete(&first).Error; err != nil {
		t.Fatal(err)
	}
	second := create("first")
	if second.Id == first.Id {
		t.Fatal("delete/recreate reused selector")
	}
}
