package board

import (
	"cmp"
	"slices"
	"strings"
)

const (
	// BSLAY_TARGET's boss numbers run to about 60
	maxBoss      = 200
	maxPoolParty = 10
	noLimit      = 0
	// a size of 0 means no limit, which still stops at Croesus's 50-player instances
	unlimitedCap = 50
)

func limit(size int) int {
	if size == noLimit {
		return unlimitedCap
	}
	return size
}

func (found *poolParty) full() bool {
	return len(found.party.Members)+len(found.reserved) >= limit(found.party.Size)
}

type Kills struct {
	Done  int `json:"done"`
	Total int `json:"total"`
}

type PoolMember struct {
	Name  string `json:"name"`
	Kills Kills  `json:"kills"`
}

type PoolParty struct {
	ID      string       `json:"id"`
	Boss    int          `json:"boss"`
	World   int          `json:"world"`
	Size    int          `json:"size"`
	Open    bool         `json:"open"`
	Members []PoolMember `json:"members"`
}

func (party PoolParty) clone() PoolParty {
	party.Members = slices.Clone(party.Members)
	return party
}

type JoinRequest struct {
	ID      string `json:"id"`
	Party   string `json:"party"`
	Name    string `json:"name"`
	Kills   Kills  `json:"kills"`
	Status  string `json:"status"`
	Created int64  `json:"created"`
}

type poolParty struct {
	party    PoolParty
	tokens   []string
	changed  uint64
	reserved []poolReservation
}

type joinRequest struct {
	request JoinRequest
	token   string
}

const maxTaskKills = 1000

func cleanKills(kills Kills) (Kills, error) {
	if kills.Total < 0 || kills.Total > maxTaskKills || kills.Done < 0 || kills.Done > kills.Total {
		return Kills{}, invalid("kills out of range")
	}
	return kills, nil
}

type NewParty struct {
	Name  string `json:"name"`
	Kills Kills  `json:"kills"`
	Boss  int    `json:"boss"`
	World int    `json:"world"`
	Size  int    `json:"size"`
	Open  bool   `json:"open"`
}

func cleanNewParty(start NewParty) (PoolMember, error) {
	name, err := CleanName(start.Name)
	if err != nil {
		return PoolMember{}, err
	}
	kills, err := cleanKills(start.Kills)
	if err != nil {
		return PoolMember{}, err
	}
	switch {
	case start.Boss < 1 || start.Boss > maxBoss:
		return PoolMember{}, invalid("a Reaper task is needed")
	case start.World < 1 || start.World > maxWorld:
		return PoolMember{}, refuse("Enter your world (1 to 259)")
	case start.Size != noLimit && (start.Size < 2 || start.Size > maxPoolParty):
		return PoolMember{}, invalid("party limit must be 2 to 10, or none")
	}
	return PoolMember{Name: name, Kills: kills}, nil
}

func (board *Board) StartPoolParty(start NewParty) (PoolParty, string, error) {
	leader, err := cleanNewParty(start)
	if err != nil {
		return PoolParty{}, "", err
	}
	board.mu.Lock()
	defer board.mu.Unlock()
	if len(board.pool) >= maxParties {
		return PoolParty{}, "", errBoardFull
	}
	token := randomHex(16)
	party := PoolParty{ID: board.freeID(), Boss: start.Boss, World: start.World, Size: start.Size, Open: start.Open,
		Members: []PoolMember{leader}}
	found := &poolParty{party: party, tokens: []string{token}}
	board.pool[party.ID] = found
	board.touchParty(found)
	board.poolMembers[token] = party.ID
	board.seen[token] = board.now()
	return party.clone(), token, nil
}

func (board *Board) RequestToJoin(partyID, name string, kills Kills, boss int) (JoinRequest, string, error) {
	name, err := CleanName(name)
	if err != nil {
		return JoinRequest{}, "", err
	}
	if kills, err = cleanKills(kills); err != nil {
		return JoinRequest{}, "", err
	}
	if boss < 1 || boss > maxBoss {
		return JoinRequest{}, "", invalid("a Reaper task is needed")
	}
	board.mu.Lock()
	defer board.mu.Unlock()
	found, ok := board.pool[partyID]
	if !ok {
		return JoinRequest{}, "", refuseAs(ErrNotFound, "This party has broken up")
	}
	if found.full() {
		return JoinRequest{}, "", refuseAs(ErrFull, "This party just filled up")
	}
	if board.waitingFor(name) >= maxWaitingPerName {
		return JoinRequest{}, "", errTooManyWaiting
	}
	token := randomHex(16)
	now := board.now()
	board.seen[token] = now
	request := JoinRequest{ID: board.freeID(), Party: partyID, Name: name, Kills: kills, Status: Pending, Created: now.Unix()}
	if found.party.Open && found.party.Boss == boss {
		board.addMember(found, PoolMember{Name: name, Kills: kills}, token)
		request.Status = Accepted
		return request, token, nil
	}
	board.requests[request.ID] = &joinRequest{request: request, token: token}
	board.requestByToken[token] = request.ID
	return request, token, nil
}

func (board *Board) addMember(found *poolParty, member PoolMember, token string) {
	found.party.Members = append(found.party.Members, member)
	board.touchParty(found)
	found.tokens = append(found.tokens, token)
	board.poolMembers[token] = found.party.ID
}

func (board *Board) leaderOf(token string) (*poolParty, error) {
	partyID, ok := board.poolMembers[token]
	if !ok {
		return nil, ErrNotFound
	}
	found := board.pool[partyID]
	if found.tokens[0] != token {
		return nil, ErrNotLeader
	}
	return found, nil
}

func (board *Board) AnswerRequest(requestID, leaderToken string, accept bool) (JoinRequest, error) {
	board.mu.Lock()
	defer board.mu.Unlock()
	asked, ok := board.requests[requestID]
	if !ok {
		return JoinRequest{}, ErrNotFound
	}
	found, err := board.leaderOf(leaderToken)
	if err != nil {
		return JoinRequest{}, err
	}
	if found.party.ID != asked.request.Party {
		return JoinRequest{}, ErrNotLeader
	}
	if asked.request.Status != Pending {
		return JoinRequest{}, ErrNotPending
	}
	if !accept {
		asked.request.Status = Declined
		return asked.request, nil
	}
	if found.full() {
		return JoinRequest{}, refuseAs(ErrFull, "Your party is already full")
	}
	board.addMember(found, PoolMember{Name: asked.request.Name, Kills: asked.request.Kills}, asked.token)
	asked.request.Status = Accepted
	delete(board.requestByToken, asked.token)
	delete(board.requests, requestID)
	return asked.request, nil
}

func (board *Board) UpdateKills(token string, kills Kills) error {
	kills, err := cleanKills(kills)
	if err != nil {
		return err
	}
	board.mu.Lock()
	defer board.mu.Unlock()
	if requestID, ok := board.requestByToken[token]; ok {
		board.requests[requestID].request.Kills = kills
		return nil
	}
	partyID, ok := board.poolMembers[token]
	if !ok {
		return ErrNotFound
	}
	found := board.pool[partyID]
	found.party.Members[slices.Index(found.tokens, token)].Kills = kills
	board.touchParty(found)
	return nil
}

func (board *Board) SetPoolPartyOpen(leaderToken string, open bool) error {
	board.mu.Lock()
	defer board.mu.Unlock()
	found, err := board.leaderOf(leaderToken)
	if err != nil {
		return err
	}
	found.party.Open = open
	board.touchParty(found)
	return nil
}

func (board *Board) LeavePool(token string) error {
	board.mu.Lock()
	defer board.mu.Unlock()
	if !board.leavePool(token) {
		return ErrNotFound
	}
	return nil
}

func (board *Board) leavePool(token string) bool {
	if requestID, ok := board.requestByToken[token]; ok {
		delete(board.requests, requestID)
		delete(board.requestByToken, token)
		delete(board.seen, token)
		delete(board.kept, token)
		return true
	}
	partyID, ok := board.poolMembers[token]
	if !ok {
		return false
	}
	found := board.pool[partyID]
	index := slices.Index(found.tokens, token)
	found.tokens = slices.Delete(found.tokens, index, index+1)
	found.party.Members = slices.Delete(found.party.Members, index, index+1)
	delete(board.poolMembers, token)
	delete(board.seen, token)
	delete(board.kept, token)
	board.touchParty(found)
	if len(found.tokens) == 0 {
		for id, asked := range board.requests {
			if asked.request.Party == partyID {
				delete(board.requestByToken, asked.token)
				delete(board.seen, asked.token)
				delete(board.kept, asked.token)
				delete(board.requests, id)
			}
		}
		board.removeParty(partyID)
	}
	return true
}

func (board *Board) sweepPool() {
	now := board.now()
	for _, found := range board.pool {
		found.reserved = slices.DeleteFunc(found.reserved, func(held poolReservation) bool { return !now.Before(held.until) })
	}
	quiet := []string{}
	for token := range board.poolMembers {
		if now.Sub(board.seen[token]) > board.timeout {
			quiet = append(quiet, token)
		}
	}
	for token := range board.requestByToken {
		if now.Sub(board.seen[token]) > board.timeout {
			quiet = append(quiet, token)
		}
	}
	for _, token := range quiet {
		board.leavePool(token)
	}
}

type PoolView struct {
	Token    string        `json:"token"`
	Party    PoolParty     `json:"party"`
	Leader   bool          `json:"leader"`
	Requests []JoinRequest `json:"requests"`
}

type RequestView struct {
	Token   string      `json:"token"`
	Request JoinRequest `json:"request"`
	Party   *PoolParty  `json:"party,omitempty"`
}

func (board *Board) poolView(token string, found *poolParty) PoolView {
	view := PoolView{Token: token, Party: found.party.clone(), Leader: found.tokens[0] == token, Requests: []JoinRequest{}}
	if view.Leader {
		for _, asked := range board.requests {
			if asked.request.Party == found.party.ID && asked.request.Status == Pending {
				view.Requests = append(view.Requests, asked.request)
			}
		}
		slices.SortFunc(view.Requests, func(first, second JoinRequest) int {
			return cmp.Or(cmp.Compare(first.Created, second.Created), strings.Compare(first.ID, second.ID))
		})
	}
	return view
}

func (board *Board) requestView(token string, asked *joinRequest) RequestView {
	view := RequestView{Token: token, Request: asked.request}
	if found, ok := board.pool[asked.request.Party]; ok {
		party := found.party.clone()
		view.Party = &party
	}
	return view
}

func (board *Board) touchParty(found *poolParty) {
	board.seq++
	found.changed = board.seq
}

func (board *Board) removeParty(id string) {
	delete(board.pool, id)
	board.seq++
	board.poolRemoved = append(board.poolRemoved, removal{id: id, seq: board.seq, at: board.now()})
}

func (board *Board) poolChanges(since uint64) (bool, []PoolParty, []string) {
	full := since < board.poolFloor || since > board.seq || since == 0
	changed, removed := []PoolParty{}, []string{}
	for _, found := range board.pool {
		if full || found.changed > since {
			changed = append(changed, found.party.clone())
		}
	}
	slices.SortFunc(changed, func(first, second PoolParty) int { return strings.Compare(first.ID, second.ID) })
	if !full {
		for _, gone := range board.poolRemoved {
			if gone.seq > since {
				removed = append(removed, gone.id)
			}
		}
	}
	return full, changed, removed
}

func (board *Board) poolPlayers() int {
	players := 0
	for _, found := range board.pool {
		players += len(found.party.Members)
	}
	return players
}
