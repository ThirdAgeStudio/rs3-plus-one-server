// only ranked players are found: not those under 11,848 XP, banned or locked, or away a long time
package names

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ThirdAgeStudio/rs3-plus-one-server/internal/limit"
)

const (
	// playerDetails lists any name that could exist, real or not; source: https://runescape.wiki/w/Application_programming_interface#Hiscores_Lite
	lookupURL = "https://secure.runescape.com/m=hiscore/index_lite.ws"
	userAgent = "rs3-plus-one-server"

	existsFor    = time.Hour
	missingFor   = 10 * time.Minute
	peekBytes    = 40
	maxAnswers   = 10_000
	lookupBurst  = 120
	lookupEvery  = time.Second
	addressBurst = 5
	addressEvery = time.Minute
	downFor      = 30 * time.Second
)

var ErrUnavailable = errors.New("player lookup unavailable")

type BusyError struct {
	RetryAfter time.Duration
}

func (busy BusyError) Error() string {
	return fmt.Sprintf("name lookups are used up for %v", busy.RetryAfter)
}

type Checker struct {
	client *http.Client
	url    string
	now    func() time.Time

	lookups    *limit.Limiter
	perAddress *limit.Limiter

	mu        sync.Mutex
	answers   map[string]answer
	downUntil time.Time
}

type answer struct {
	exists bool
	until  time.Time
}

func New(client *http.Client, perAddress bool) *Checker {
	checker := &Checker{client: client, url: lookupURL, now: time.Now, answers: map[string]answer{}}
	clock := func() time.Time { return checker.now() }
	checker.lookups = limit.New(lookupBurst, lookupEvery, clock)
	if perAddress {
		checker.perAddress = limit.New(addressBurst, addressEvery, clock)
	}
	return checker
}

func (checker *Checker) Exists(ctx context.Context, name, address string) (bool, error) {
	key := normalise(name)
	checker.mu.Lock()
	known, ok := checker.answers[key]
	down := checker.now().Before(checker.downUntil)
	checker.mu.Unlock()
	switch {
	case ok && checker.now().Before(known.until):
		return known.exists, nil
	case down:
		return false, ErrUnavailable
	}

	var wait time.Duration
	if checker.perAddress != nil {
		wait = checker.perAddress.Take(address)
	}
	if wait == 0 {
		wait = checker.lookups.Take("")
	}
	if wait > 0 {
		return false, BusyError{RetryAfter: wait}
	}
	exists, err := checker.lookup(ctx, name)
	if err != nil {
		checker.mu.Lock()
		checker.downUntil = checker.now().Add(downFor)
		checker.mu.Unlock()
		return false, err
	}
	keep := missingFor
	if exists {
		keep = existsFor
	}
	checker.remember(key, answer{exists: exists, until: checker.now().Add(keep)})
	return exists, nil
}

func (checker *Checker) remember(key string, known answer) {
	checker.mu.Lock()
	defer checker.mu.Unlock()
	if len(checker.answers) >= maxAnswers {
		now := checker.now()
		for old, stored := range checker.answers {
			if !now.Before(stored.until) {
				delete(checker.answers, old)
			}
		}
	}
	checker.answers[key] = known
}

func (checker *Checker) lookup(ctx context.Context, name string) (bool, error) {
	query := url.Values{"player": {name}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, checker.url+"?"+query.Encode(), nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := checker.client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNotFound:
		return false, nil
	case http.StatusOK:
	default:
		return false, fmt.Errorf("hiscores answered %d", response.StatusCode)
	}
	// a bot check page can come back as a 200 too, so only an answer starting with stats counts
	body, err := io.ReadAll(io.LimitReader(response.Body, peekBytes))
	if err != nil {
		return false, err
	}
	if !strings.ContainsAny(string(body[:min(1, len(body))]), "-0123456789") {
		return false, fmt.Errorf("hiscores answered %q", body)
	}
	return true, nil
}

func normalise(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", " "))
}
