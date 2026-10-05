package ai

import (
	_ "embed"
	"encoding/json"
	"sync"
)

//go:embed models_catalog.json
var modelsCatalogJSON []byte

var loadCatalogOnce sync.Once

// LoadBuiltinModels registers pi's generated model catalog (idempotent). It is
// invoked automatically by GetModel/GetModels/GetProviders.
func LoadBuiltinModels() {
	loadCatalogOnce.Do(func() {
		var catalog map[string]map[string]*Model
		if err := json.Unmarshal(modelsCatalogJSON, &catalog); err != nil {
			// The catalog is a compile-time embed regenerated from pi's npm
			// build; corruption is a programmer error and must fail loud (a
			// silent empty catalog masked behind GetModel nils otherwise).
			panic("ai: embedded models_catalog.json is corrupt: " + err.Error())
		}
		for provider, models := range catalog {
			for id, m := range models {
				if m.Provider == "" {
					m.Provider = provider
				}
				if m.ID == "" {
					m.ID = id
				}
				RegisterModel(m)
			}
		}
	})
}
