//go:build dev

package board

func (board *Board) TrackPollers() {
	board.mu.Lock()
	defer board.mu.Unlock()
	board.pollers = map[string][]string{}
}

func (board *Board) HostedBy(client string) (Listing, bool) {
	board.mu.Lock()
	defer board.mu.Unlock()
	var newest *entry
	for _, token := range board.pollers[client] {
		if id, ok := board.byToken[token]; ok && (newest == nil || board.listings[id].listing.Created > newest.listing.Created) {
			newest = board.listings[id]
		}
	}
	if newest == nil {
		return Listing{}, false
	}
	return newest.listing.clone(), true
}

func (board *Board) PartyOf(client string) (PoolParty, bool) {
	board.mu.Lock()
	defer board.mu.Unlock()
	for _, token := range board.pollers[client] {
		if id, ok := board.poolMembers[token]; ok {
			return board.pool[id].party.clone(), true
		}
	}
	return PoolParty{}, false
}

func (board *Board) Listings() []Listing {
	board.mu.Lock()
	defer board.mu.Unlock()
	all := []Listing{}
	for _, found := range board.listings {
		all = append(all, found.listing.clone())
	}
	return all
}

func (board *Board) Keep(token string, keep bool) {
	board.mu.Lock()
	defer board.mu.Unlock()
	if keep {
		board.kept[token] = true
	} else {
		delete(board.kept, token)
	}
}
