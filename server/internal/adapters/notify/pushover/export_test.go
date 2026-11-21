package pushover

import "github.com/pmoscode/SerMon/server/internal/platform/safehttp"

// NewWithAPI points the notifier to a test server instead of the Pushover API.
func NewWithAPI(client *safehttp.Client, api string) *Notifier {
	return &Notifier{client: client, api: api}
}
