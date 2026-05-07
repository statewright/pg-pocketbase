//go:build !postgres

package apis

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// storeAppleRedirectName stores the Apple user's name in the local app.Store().
// This is the original PocketBase behavior — suitable for single-instance SQLite deployments.
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

	app.Store().Set(nameKey, fullName)
	time.AfterFunc(1*time.Minute, func() {
		app.Store().Remove(nameKey)
	})
	return nil
}

// retrieveAppleRedirectName retrieves the Apple user's name from the local app.Store().
func retrieveAppleRedirectName(app core.App, nameKey string) (string, bool) {
	name, ok := app.Store().Get(nameKey).(string)
	if ok {
		app.Store().Remove(nameKey)
	}
	return name, ok
}
