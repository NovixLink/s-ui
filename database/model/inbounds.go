package model

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
)

type Inbound struct {
	Id   uint   `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	Type string `json:"type" form:"type"`
	Tag  string `json:"tag" form:"tag" gorm:"unique"`

	// Foreign key to tls table
	TlsId uint `json:"tls_id" form:"tls_id"`
	Tls   *Tls `json:"tls" form:"tls" gorm:"foreignKey:TlsId;references:Id"`

	Addrs   json.RawMessage `json:"addrs" form:"addrs"`
	OutJson json.RawMessage `json:"out_json" form:"out_json"`
	Options json.RawMessage `json:"-" form:"-"`
}

// ApplyRealityShortID derives the stable line selector from the existing
// AUTOINCREMENT primary key. Renames and edits preserve it; deleting and
// recreating an inbound gives it a new selector. It does not authenticate users.
func (i Inbound) ApplyRealityShortID(tls map[string]interface{}, server bool) string {
	if i.Type != "vless" || i.Id == 0 {
		return ""
	}
	reality, ok := tls["reality"].(map[string]interface{})
	if !ok || reality["enabled"] != true {
		return ""
	}
	var identifier [8]byte
	binary.BigEndian.PutUint64(identifier[:], uint64(i.Id))
	shortID := hex.EncodeToString(identifier[:])
	if server {
		reality["short_id"] = []string{shortID}
	} else {
		reality["short_id"] = shortID
	}
	return shortID
}

func (i *Inbound) UnmarshalJSON(data []byte) error {
	var err error
	var raw map[string]interface{}
	if err = json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// Extract fixed fields and store the rest in Options
	if val, exists := raw["id"].(float64); exists {
		i.Id = uint(val)
	}
	delete(raw, "id")
	i.Type, _ = raw["type"].(string)
	delete(raw, "type")
	i.Tag, _ = raw["tag"].(string)
	delete(raw, "tag")

	// TlsId
	if val, exists := raw["tls_id"].(float64); exists {
		i.TlsId = uint(val)
	}
	delete(raw, "tls_id")
	delete(raw, "tls")
	delete(raw, "users")

	// Addrs
	i.Addrs, _ = json.MarshalIndent(raw["addrs"], "", "  ")
	delete(raw, "addrs")

	// OutJson
	i.OutJson, _ = json.MarshalIndent(raw["out_json"], "", "  ")
	delete(raw, "out_json")

	// Remaining fields
	i.Options, err = json.MarshalIndent(raw, "", "  ")
	return err
}

// MarshalJSON customizes marshalling
func (i Inbound) MarshalJSON() ([]byte, error) {
	// Combine fixed fields and dynamic fields into one map
	combined := make(map[string]interface{})
	combined["type"] = i.Type
	combined["tag"] = i.Tag
	if i.Tls != nil {
		var tls map[string]interface{}
		if err := json.Unmarshal(i.Tls.Server, &tls); err != nil {
			return nil, err
		}
		i.ApplyRealityShortID(tls, true)
		combined["tls"] = tls
	}

	if i.Options != nil {
		var restFields map[string]json.RawMessage
		if err := json.Unmarshal(i.Options, &restFields); err != nil {
			return nil, err
		}

		for k, v := range restFields {
			combined[k] = v
		}
	}

	return json.Marshal(combined)
}

func (i Inbound) MarshalFull() (*map[string]interface{}, error) {
	combined := make(map[string]interface{})
	combined["id"] = i.Id
	combined["type"] = i.Type
	combined["tag"] = i.Tag
	combined["tls_id"] = i.TlsId
	combined["addrs"] = i.Addrs
	combined["out_json"] = i.OutJson

	if i.Options != nil {
		var restFields map[string]interface{}
		if err := json.Unmarshal(i.Options, &restFields); err != nil {
			return nil, err
		}

		for k, v := range restFields {
			combined[k] = v
		}
	}
	return &combined, nil
}
