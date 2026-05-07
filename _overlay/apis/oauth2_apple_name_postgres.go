//go:build postgres

package apis

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

const appleNameTTL = 1 * time.Minute

// storeAppleRedirectName stores the Apple user's name in the PG-backed temp KV
// (falls back to app.Store() if TempKV is not available).
func storeAppleRedirectName(app core.App, nameKey string, serializedNameData string) error {
	if serializedNameData == "" {
		return nil
	}

	if len(nameKey) > 1000 {
		return errors.New("nameKey is too large")
	}

	extracted := struct {
		Name struct {
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
		} `json:"name"`
	}{}
	if err := json.Unmarshal([]byte(serializedNameData), &extracted); err != nil {
		return err
	}

	fullName := extracted.Name.FirstName + " " + extracted.Name.LastName
	if len(fullName) > 150 {
		fullName = fullName[:150]
	}
	fullName = strings.TrimSpace(fullName)
	if fullName == "" {
		return nil
	}

	// Try PG-backed temp KV first
	if kv := getTempKV(app); kv != nil {
		return kv.Set(nameKey, fullName, appleNameTTL)
	}

	// Fallback: local app.Store()
	app.Store().Set(nameKey, fullName)
	time.AfterFunc(appleNameTTL, func() {
		app.Store().Remove(nameKey)
	})
	return nil
}

// retrieveAppleRedirectName retrieves the Apple user's name from PG-backed temp KV
// (falls back to app.Store() if TempKV is not available).
func retrieveAppleRedirectName(app core.App, nameKey string) (string, bool) {
	// Try PG-backed temp KV first
	if kv := getTempKV(app); kv != nil {
		name, ok := kv.Get(nameKey)
		if ok {
			kv.Delete(nameKey)
		}
		return name, ok
	}

	// Fallback: local app.Store()
	name, ok := app.Store().Get(nameKey).(string)
	if ok {
		app.Store().Remove(nameKey)
	}
	return name, ok
}

type tempKVInterface interface {
	Set(key, value string, ttl time.Duration) error
	Get(key string) (string, bool)
	Delete(key string)
}

func getTempKV(app core.App) tempKVInterface {
	kv, _ := app.Store().Get("pgpb_tempkv").(tempKVInterface)
	return kv
}
