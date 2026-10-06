package apiclient

import (
	"net/http/cookiejar"
	"sync"
)

// jarsByKey holds one persistent, in-memory cookie jar per RequestSpec.
// CookieJarKey — previously Execute always built a brand-new http.Client
// per call with no jar at all, so a login response's Set-Cookie could never
// carry into a later request; a session-cookie-based auth flow simply had
// no way to work. Jars live only for the process's lifetime (not persisted
// to disk) — acceptable since they're ephemeral session state, not
// configuration a user would expect to survive a restart the way
// collections/environments do.
var (
	jarsMu    sync.Mutex
	jarsByKey = map[string]*cookiejar.Jar{}
)

// jarFor returns the persistent jar for key, creating one on first use.
// cookiejar.New's only possible error is a bad PublicSuffixList option,
// which is never provided here (nil), so the error is unreachable and
// safely ignored rather than threading it through every caller.
func jarFor(key string) *cookiejar.Jar {
	jarsMu.Lock()
	defer jarsMu.Unlock()
	if j, ok := jarsByKey[key]; ok {
		return j
	}
	j, _ := cookiejar.New(nil)
	jarsByKey[key] = j
	return j
}
