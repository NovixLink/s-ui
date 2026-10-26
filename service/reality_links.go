package service

import (
	"encoding/json"
	"net/url"

	"github.com/alireza0/s-ui/database"
	"github.com/alireza0/s-ui/database/model"
	"gorm.io/gorm"
)

// SyncRealityShortIDs backfills the panel's existing generated projections.
// It changes only REALITY sid values, preserving addresses, ports, user UUIDs,
// external links and routing. The inbound primary key remains the sole authority.
func (s *InboundService) SyncRealityShortIDs() error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var inbounds []model.Inbound
		if err := tx.Preload("Tls").Where("type = ?", "vless").Find(&inbounds).Error; err != nil {
			return err
		}
		selectors := make(map[string]string)
		for _, in := range inbounds {
			if in.Tls == nil {
				continue
			}
			var server map[string]interface{}
			if err := json.Unmarshal(in.Tls.Server, &server); err != nil {
				return err
			}
			selector := in.ApplyRealityShortID(server, false)
			if selector == "" {
				continue
			}
			selectors[in.Tag] = selector
			var out map[string]interface{}
			if len(in.OutJson) == 0 {
				continue
			}
			if err := json.Unmarshal(in.OutJson, &out); err != nil {
				return err
			}
			outTLS, ok := out["tls"].(map[string]interface{})
			if !ok {
				continue
			}
			reality, ok := outTLS["reality"].(map[string]interface{})
			if !ok || reality["short_id"] == selector {
				continue
			}
			in.ApplyRealityShortID(outTLS, false)
			encoded, err := json.Marshal(out)
			if err != nil {
				return err
			}
			if err = tx.Model(&model.Inbound{}).Where("id = ?", in.Id).Update("out_json", encoded).Error; err != nil {
				return err
			}
		}
		if len(selectors) == 0 {
			return nil
		}
		var clients []model.Client
		if err := tx.Select("id", "links").Find(&clients).Error; err != nil {
			return err
		}
		for _, client := range clients {
			var links []map[string]string
			if len(client.Links) == 0 {
				continue
			}
			if err := json.Unmarshal(client.Links, &links); err != nil {
				return err
			}
			changed := false
			for _, link := range links {
				selector := selectors[link["remark"]]
				if link["type"] != "local" || selector == "" {
					continue
				}
				uri, err := url.Parse(link["uri"])
				if err != nil || uri.Scheme != "vless" {
					continue
				}
				query := uri.Query()
				if query.Get("security") != "reality" || query.Get("sid") == selector {
					continue
				}
				query.Set("sid", selector)
				uri.RawQuery = query.Encode()
				link["uri"] = uri.String()
				changed = true
			}
			if changed {
				encoded, err := json.Marshal(links)
				if err != nil {
					return err
				}
				if err = tx.Model(&model.Client{}).Where("id = ?", client.Id).Update("links", encoded).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}
