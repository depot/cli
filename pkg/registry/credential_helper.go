package registry

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/types"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/moby/buildkit/util/progress/progresswriter"
)

// credentialHelperTimeout is how long a Docker credential helper may take to
// answer. The builder abandons a client auth call 60s after starting it, and
// that window includes up to 5s waiting for the session plus the round trip.
var credentialHelperTimeout = 50 * time.Second

// credentialCacheTTL matches buildkit's default refresh window, since some
// helpers hand out tokens that expire after five minutes.
const credentialCacheTTL = 4*time.Minute + 50*time.Second

// credentialLookup runs Docker credential helpers with a time limit, because
// Docker's helper client has none.
type credentialLookup struct {
	config *configfile.ConfigFile

	mu      sync.Mutex
	entries map[string]*credentialEntry
	// Calls to one helper run one at a time (docker/cli#1862); different
	// helpers run in parallel.
	helperSlots map[string]chan struct{}
	hungHelpers map[string]bool
	logger      progresswriter.Logger
}

type credentialEntry struct {
	done          chan struct{}
	deadline      time.Time
	loadedAt      time.Time
	authConfig    types.AuthConfig
	err           error
	timeoutLogged bool
	// loading is true while a goroutine owns this lookup; running once it is
	// calling the helper.
	loading bool
	running bool
	// previous is the last answered lookup, used if a refresh times out.
	previous *credentialEntry
}

func newCredentialLookup(cfg *configfile.ConfigFile) *credentialLookup {
	return &credentialLookup{
		config:      cfg,
		entries:     map[string]*credentialEntry{},
		helperSlots: map[string]chan struct{}{},
		hungHelpers: map[string]bool{},
	}
}

func (l *credentialLookup) setLogger(logger progresswriter.Logger) {
	l.mu.Lock()
	l.logger = logger
	l.mu.Unlock()
}

// get returns the auth config for host, or an empty one if the helper does
// not answer in time.
func (l *credentialLookup) get(ctx context.Context, host string) (types.AuthConfig, error) {
	key := configKey(host)
	helper := l.config.CredentialsStore
	if h, ok := l.config.CredentialHelpers[key]; ok {
		helper = h
	}

	l.mu.Lock()
	entry := l.entries[key]
	// A lookup that never ran (helper hung or busy) is retried once the helper answers again.
	if entry == nil || entry.expired() || (!entry.loading && !entry.answered() && !l.hungHelpers[helper]) {
		next := &credentialEntry{done: make(chan struct{}), deadline: time.Now().Add(credentialHelperTimeout)}
		if entry != nil {
			next.previous = entry.previous
			if entry.answered() && entry.err == nil {
				next.previous = entry
			}
		}
		entry = next
		l.entries[key] = entry
		if l.hungHelpers[helper] {
			entry.deadline = time.Now()
		} else {
			slot, ok := l.helperSlots[helper]
			if !ok {
				slot = make(chan struct{}, 1)
				l.helperSlots[helper] = slot
			}
			entry.loading = true
			go l.load(entry, slot, helper, key)
		}
	}
	l.mu.Unlock()

	timer := time.NewTimer(time.Until(entry.deadline))
	defer timer.Stop()
	select {
	case <-entry.done:
		return entry.authConfig, entry.err
	case <-ctx.Done():
		return types.AuthConfig{}, ctx.Err()
	case <-timer.C:
	}

	l.mu.Lock()
	select {
	case <-entry.done:
		l.mu.Unlock()
		return entry.authConfig, entry.err
	default:
	}
	// Only this lookup's own helper call still running proves the helper is
	// hung; its load clears the flag when the call returns.
	if helper != "" && entry.running {
		l.hungHelpers[helper] = true
	}
	if entry.previous != nil {
		previous := entry.previous
		l.mu.Unlock()
		return previous.authConfig, previous.err
	}
	logTimeout := !entry.timeoutLogged
	entry.timeoutLogged = true
	logger := l.logger
	l.mu.Unlock()

	if logTimeout {
		message := fmt.Sprintf("[auth] docker-credential-%s did not respond within %s for %s; continuing without credentials for this host", helper, credentialHelperTimeout, host)
		if helper == "" {
			message = fmt.Sprintf("[auth] Docker credential lookup did not respond within %s for %s; continuing without credentials for this host", credentialHelperTimeout, host)
		}
		if logger != nil {
			progresswriter.Wrap(message, logger, func(progresswriter.SubLogger) error { return nil })
		} else {
			fmt.Fprintln(os.Stderr, message)
		}
	}
	return types.AuthConfig{}, nil
}

// cached returns what get last found for host without running a helper. The
// inner provider calls it while holding its lock, after get has waited.
func (l *credentialLookup) cached(host string) (types.AuthConfig, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[configKey(host)]
	switch {
	case entry == nil:
		return types.AuthConfig{}, nil
	case entry.answered():
		return entry.authConfig, entry.err
	case entry.previous != nil:
		return entry.previous.authConfig, entry.previous.err
	default:
		return types.AuthConfig{}, nil
	}
}

func (l *credentialLookup) load(entry *credentialEntry, slot chan struct{}, helper, key string) {
	timer := time.NewTimer(time.Until(entry.deadline))
	defer timer.Stop()
	select {
	case slot <- struct{}{}:
		l.mu.Lock()
		entry.running = true
		l.mu.Unlock()
	case <-timer.C:
		// The helper is still busy with an earlier host and has not answered.
		l.mu.Lock()
		entry.loading = false
		l.mu.Unlock()
		return
	}
	authConfig, err := l.config.GetAuthConfig(key)
	<-slot

	l.mu.Lock()
	entry.authConfig, entry.err, entry.loadedAt = authConfig, err, time.Now()
	entry.loading, entry.running = false, false
	delete(l.hungHelpers, helper)
	close(entry.done)
	l.mu.Unlock()
}

// expired and answered are called with credentialLookup.mu held. Errors are
// not cached, so the next call asks the helper again.
func (e *credentialEntry) expired() bool {
	return e.answered() && (e.err != nil || time.Since(e.loadedAt) > credentialCacheTTL)
}

func (e *credentialEntry) answered() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// configKey maps a registry host to its Docker config key, accepting either
// form so get and cached agree however the caller names Docker Hub.
func configKey(host string) string {
	if host == authprovider.DockerHubRegistryHost {
		return authprovider.DockerHubConfigfileKey
	}
	return host
}
