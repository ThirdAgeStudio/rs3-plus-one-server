// a request can't be proven to come from the plugin, so every field is checked here
package board

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	DefaultTimeout = 3 * time.Minute
	partyCap       = 4 * time.Hour
	onlineWindow   = 2 * time.Minute
	removalsKept   = 10 * time.Minute
	restoreWindow  = 5 * time.Minute
	SweepEvery     = 10 * time.Second

	maxListingsPerName = 2
	maxWaitingPerName  = 5
	maxListings        = 2_000
	maxParties         = 2_000
)

var errBoardFull = refuseAs(ErrBoardFull, "The board is full, try again soon")

func (board *Board) listingsHostedBy(name string) int {
	count := 0
	for _, found := range board.listings {
		if sameName(found.listing.Host, name) {
			count++
		}
	}
	return count
}

func (board *Board) waitingFor(name string) int {
	count := 0
	for _, found := range board.applications {
		if found.application.Status == Pending && sameName(found.application.Applicant, name) {
			count++
		}
	}
	for _, asked := range board.requests {
		if asked.request.Status == Pending && sameName(asked.request.Name, name) {
			count++
		}
	}
	return count
}

var errTooManyWaiting = refuseAs(ErrTooMany, "You have too many applications waiting")

type entry struct {
	listing      Listing
	token        string
	changed      uint64
	applications []string
	reserved     []reservation
}

type removal struct {
	id  string
	seq uint64
	at  time.Time
}

type Board struct {
	mu             sync.Mutex
	now            func() time.Time
	timeout        time.Duration
	restoreUntil   time.Time
	swept          time.Time
	seq            uint64
	floor          uint64
	listings       map[string]*entry
	byToken        map[string]string
	applications   map[string]*application
	appByToken     map[string]string
	seen           map[string]time.Time
	kept           map[string]bool
	removed        []removal
	poolFloor      uint64
	poolRemoved    []removal
	pool           map[string]*poolParty
	poolMembers    map[string]string
	requests       map[string]*joinRequest
	requestByToken map[string]string
	clients        map[string]time.Time
	pollers        map[string][]string
}

func New(now func() time.Time, timeout time.Duration) *Board {
	return &Board{
		// a cursor of 0 is a first poll, so the sequence starts past it
		now: now, timeout: timeout, restoreUntil: now().Add(restoreWindow), seq: 1,
		listings: map[string]*entry{}, byToken: map[string]string{},
		applications: map[string]*application{}, appByToken: map[string]string{},
		seen: map[string]time.Time{}, kept: map[string]bool{},
		pool: map[string]*poolParty{}, poolMembers: map[string]string{},
		requests: map[string]*joinRequest{}, requestByToken: map[string]string{},
		clients: map[string]time.Time{},
	}
}

func randomHex(bytes int) string {
	buffer := make([]byte, bytes)
	rand.Read(buffer)
	return hex.EncodeToString(buffer)
}

func (board *Board) Post(fields Listing) (Listing, string, error) {
	listing, err := clean(fields)
	if err != nil {
		return Listing{}, "", err
	}
	board.mu.Lock()
	defer board.mu.Unlock()
	switch {
	case len(board.listings) >= maxListings:
		return Listing{}, "", errBoardFull
	case board.listingsHostedBy(listing.Host) >= maxListingsPerName:
		return Listing{}, "", refuseAs(ErrTooMany, "You already have a listing up")
	}
	token := randomHex(16)
	now := board.now()
	listing.ID, listing.Created = board.freeID(), now.Unix()
	board.seq++
	board.listings[listing.ID] = &entry{listing: listing, token: token, changed: board.seq}
	board.byToken[token] = listing.ID
	board.seen[token] = now
	return listing.clone(), token, nil
}

func (board *Board) live(token string) bool {
	_, listing := board.byToken[token]
	_, application := board.appByToken[token]
	_, member := board.poolMembers[token]
	_, request := board.requestByToken[token]
	return listing || application || member || request
}

func (board *Board) idTaken(id string) bool {
	_, listing := board.listings[id]
	_, application := board.applications[id]
	_, party := board.pool[id]
	_, request := board.requests[id]
	return listing || application || party || request
}

func (board *Board) freeID() string {
	for {
		id := randomHex(6)
		if !board.idTaken(id) {
			return id
		}
	}
}

var displayName = regexp.MustCompile(`^[A-Za-z0-9 _-]{1,12}$`)

func CleanName(name string) (string, error) {
	// the game gives some spaces in names as non-breaking spaces
	name = strings.TrimSpace(strings.ReplaceAll(name, "\u00a0", " "))
	if !displayName.MatchString(name) {
		return "", invalid("not a display name")
	}
	return name, nil
}

func validToken(token string) bool   { return isHex(token, 32) }
func ValidClient(client string) bool { return isHex(client, 16) }
func validID(id string) bool         { return isHex(id, 12) }

func isHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (board *Board) touch(host *entry) {
	board.seq++
	host.changed = board.seq
}

func (board *Board) Close(id, token string) error {
	board.mu.Lock()
	defer board.mu.Unlock()
	found, ok := board.listings[id]
	if !ok {
		return ErrNotFound
	}
	if found.token != token {
		return ErrWrongKey
	}
	board.remove(id)
	return nil
}

func (board *Board) remove(id string) {
	found := board.listings[id]
	for _, applicationID := range slices.Clone(found.applications) {
		board.dropApplication(applicationID)
	}
	delete(board.byToken, found.token)
	delete(board.seen, found.token)
	delete(board.kept, found.token)
	delete(board.listings, id)
	board.seq++
	board.removed = append(board.removed, removal{id: id, seq: board.seq, at: board.now()})
}

func (board *Board) Sweep() {
	board.mu.Lock()
	defer board.mu.Unlock()
	board.sweep()
}

func (board *Board) sweep() {
	now := board.now()
	board.swept = now
	for token := range board.kept {
		board.seen[token] = now
	}
	for id, found := range board.listings {
		board.releaseReserved(found, now)
		quiet := now.Sub(board.seen[found.token]) > board.timeout
		if quiet || now.Sub(time.Unix(found.listing.Created, 0)) > partyCap {
			board.remove(id)
		}
	}
	for id, found := range board.applications {
		if now.Sub(board.seen[found.token]) > board.timeout {
			board.withdraw(id)
		}
	}
	board.sweepPool()
	for client, seen := range board.clients {
		if now.Sub(seen) > onlineWindow {
			delete(board.clients, client)
			delete(board.pollers, client)
		}
	}
	gone := 0
	for gone < len(board.removed) && now.Sub(board.removed[gone].at) > removalsKept {
		board.floor = board.removed[gone].seq
		gone++
	}
	board.removed = board.removed[gone:]
	gone = 0
	for gone < len(board.poolRemoved) && now.Sub(board.poolRemoved[gone].at) > removalsKept {
		board.poolFloor = board.poolRemoved[gone].seq
		gone++
	}
	board.poolRemoved = board.poolRemoved[gone:]
}

type PollRequest struct {
	Tokens    []string `json:"tokens"`
	Since     uint64   `json:"since"`
	Board     bool     `json:"board"`
	Pool      bool     `json:"pool"`
	PoolSince uint64   `json:"poolSince"`
	Client    string   `json:"client"`
}

type PollResult struct {
	Cursor      uint64
	Full        bool
	Changed     []Listing
	Removed     []string
	Unknown     []string
	Hosting     []HostView
	Applied     []ApplicantView
	Listings    int
	InPool      []PoolView
	Asked       []RequestView
	PoolFull    bool
	PoolChanged []PoolParty
	PoolRemoved []string
	PoolPlayers int
	Online      int
}

func (board *Board) Poll(request PollRequest) PollResult {
	board.mu.Lock()
	defer board.mu.Unlock()
	now := board.now()
	// a host can pause the sweep's timer between requests (Cloud Run does), so a poll catches up
	if now.Sub(board.swept) >= SweepEvery {
		board.sweep()
	}
	result := PollResult{Cursor: board.seq, Listings: len(board.listings)}
	if ValidClient(request.Client) {
		board.clients[request.Client] = now
		if board.pollers != nil {
			board.pollers[request.Client] = slices.Clone(request.Tokens)
		}
	}
	result.Online = len(board.clients)
	result.PoolPlayers = board.poolPlayers()
	if request.Pool {
		result.PoolFull, result.PoolChanged, result.PoolRemoved = board.poolChanges(request.PoolSince)
	}
	for _, token := range request.Tokens {
		if id, ok := board.byToken[token]; ok {
			board.seen[token] = now
			result.Hosting = append(result.Hosting, board.hostView(token, board.listings[id]))
		} else if id, ok := board.appByToken[token]; ok {
			board.seen[token] = now
			result.Applied = append(result.Applied, board.applicantView(token, board.applications[id]))
		} else if id, ok := board.poolMembers[token]; ok {
			board.seen[token] = now
			result.InPool = append(result.InPool, board.poolView(token, board.pool[id]))
		} else if id, ok := board.requestByToken[token]; ok {
			board.seen[token] = now
			result.Asked = append(result.Asked, board.requestView(token, board.requests[id]))
		} else {
			result.Unknown = append(result.Unknown, token)
		}
	}
	if !request.Board {
		return result
	}
	result.Changed, result.Removed = []Listing{}, []string{}
	result.Full = request.Since < board.floor || request.Since > board.seq || request.Since == 0
	for _, found := range board.listings {
		if result.Full || found.changed > request.Since {
			result.Changed = append(result.Changed, found.listing.clone())
		}
	}
	if !result.Full {
		for _, gone := range board.removed {
			if gone.seq > request.Since {
				result.Removed = append(result.Removed, gone.id)
			}
		}
	}
	return result
}
