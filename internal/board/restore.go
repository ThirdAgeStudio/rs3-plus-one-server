package board

import (
	"slices"
	"strings"
	"time"
)

// restores are only taken just after a start, so they can't be used to get round the usual checks
const (
	RestoreMember = "member"
	RestoreAsking = "asking"
)

type reservation struct {
	member Member
	until  time.Time
}

type poolReservation struct {
	member PoolMember
	until  time.Time
}

func (board *Board) restoring(token string) error {
	switch {
	case !board.now().Before(board.restoreUntil):
		return refuseAs(ErrGone, "The server restarted too long ago, so this can't be restored")
	case !validToken(token):
		return invalid("malformed token")
	case board.live(token):
		return ErrTokenLive
	}
	return nil
}

func (board *Board) restoringID(id string) error {
	switch {
	case !validID(id):
		return invalid("malformed id")
	case board.idTaken(id):
		return ErrIDTaken
	}
	return nil
}

func (board *Board) RestoreListing(fields Listing, token string) (Listing, error) {
	listing, known, err := cleanShape(fields)
	if err != nil {
		return Listing{}, err
	}
	if listing.Filled < 1 || listing.Filled > listing.Size || len(fields.Members) > listing.Filled-1 {
		return Listing{}, invalid("filled out of range")
	}
	reserved := make([]Member, len(fields.Members))
	for index, member := range fields.Members {
		if reserved[index].Name, err = CleanName(member.Name); err != nil {
			return Listing{}, err
		}
		if reserved[index].Roles, err = knownRoles(member.Roles, known); err != nil {
			return Listing{}, err
		}
	}
	board.mu.Lock()
	defer board.mu.Unlock()
	if err := board.restoring(token); err != nil {
		return Listing{}, err
	}
	if err := board.restoringID(fields.ID); err != nil {
		return Listing{}, err
	}
	now := board.now()
	listing.ID, listing.Created = fields.ID, now.Unix()
	// the party cap still counts from the first post
	if fields.Created > 0 && fields.Created < listing.Created {
		listing.Created = fields.Created
	}
	host := &entry{listing: listing, token: token}
	for _, member := range reserved {
		host.reserved = append(host.reserved, reservation{member: member, until: now.Add(board.timeout)})
	}
	board.listings[listing.ID] = host
	board.touch(host)
	board.byToken[token] = listing.ID
	board.seen[token] = now
	return listing.clone(), nil
}

func (board *Board) RestoreApplication(fields Application, token string) (Application, error) {
	board.mu.Lock()
	defer board.mu.Unlock()
	if err := board.restoring(token); err != nil {
		return Application{}, err
	}
	if err := board.restoringID(fields.ID); err != nil {
		return Application{}, err
	}
	host, ok := board.listings[fields.Listing]
	if !ok {
		return Application{}, ErrNotFound
	}
	switch fields.Status {
	case Pending:
		// accepted while its plugin wasn't polling, so it still says pending
		if reservedFor(host, fields) >= 0 {
			return board.claimSpot(host, fields, token)
		}
		return board.apply(fields.Listing, fields, fields.ID, token)
	case Accepted:
		return board.claimSpot(host, fields, token)
	}
	return Application{}, invalid("only pending and accepted applications are restored")
}

func (board *Board) claimSpot(host *entry, fields Application, token string) (Application, error) {
	name, err := CleanName(fields.Applicant)
	if err != nil {
		return Application{}, err
	}
	if err := checkStats(fields); err != nil {
		return Application{}, err
	}
	fields.Applicant = name
	index := reservedFor(host, fields)
	if index < 0 {
		return Application{}, refuseAs(ErrGone, "Your place in this party wasn't kept")
	}
	member := host.reserved[index].member
	host.reserved = slices.Delete(host.reserved, index, index+1)
	host.listing.Members = append(host.listing.Members, member)
	board.touch(host)
	claimed := Application{ID: fields.ID, Listing: fields.Listing, Applicant: member.Name, Roles: member.Roles,
		Kills: fields.Kills, Combat: fields.Combat, Status: Accepted}
	return board.addApplication(host, claimed, token), nil
}

func reservedFor(host *entry, fields Application) int {
	return slices.IndexFunc(host.reserved, func(held reservation) bool {
		return sameName(held.member.Name, fields.Applicant) && sameRoles(held.member.Roles, fields.Roles)
	})
}

func (board *Board) releaseReserved(host *entry, now time.Time) {
	for _, held := range host.reserved {
		if !now.Before(held.until) {
			host.listing.Filled--
			host.listing.Needs = append(host.listing.Needs, held.member.Roles...)
			board.touch(host)
		}
	}
	host.reserved = slices.DeleteFunc(host.reserved, func(held reservation) bool { return !now.Before(held.until) })
}

func (board *Board) RestorePoolParty(start NewParty, id, token string, members []PoolMember) (PoolParty, error) {
	leader, err := cleanNewParty(start)
	if err != nil {
		return PoolParty{}, err
	}
	if len(members) > limit(start.Size)-1 {
		return PoolParty{}, invalid("more members than the party takes")
	}
	reserved := make([]PoolMember, len(members))
	for index, member := range members {
		if reserved[index].Name, err = CleanName(member.Name); err != nil {
			return PoolParty{}, err
		}
		if reserved[index].Kills, err = cleanKills(member.Kills); err != nil {
			return PoolParty{}, err
		}
	}
	board.mu.Lock()
	defer board.mu.Unlock()
	if err := board.restoring(token); err != nil {
		return PoolParty{}, err
	}
	if err := board.restoringID(id); err != nil {
		return PoolParty{}, err
	}
	now := board.now()
	found := &poolParty{party: PoolParty{ID: id, Boss: start.Boss, World: start.World, Size: start.Size, Open: start.Open,
		Members: []PoolMember{leader}}, tokens: []string{token}}
	for _, member := range reserved {
		found.reserved = append(found.reserved, poolReservation{member: member, until: now.Add(board.timeout)})
	}
	board.pool[id] = found
	board.touchParty(found)
	board.poolMembers[token] = id
	board.seen[token] = now
	return found.party.clone(), nil
}

func (board *Board) RejoinPool(partyID, requestID, token, name string, kills Kills, boss int, status string) (JoinRequest, error) {
	name, err := CleanName(name)
	if err != nil {
		return JoinRequest{}, err
	}
	if kills, err = cleanKills(kills); err != nil {
		return JoinRequest{}, err
	}
	if boss < 1 || boss > maxBoss {
		return JoinRequest{}, invalid("a Reaper task is needed")
	}
	board.mu.Lock()
	defer board.mu.Unlock()
	if err := board.restoring(token); err != nil {
		return JoinRequest{}, err
	}
	found, ok := board.pool[partyID]
	if !ok {
		return JoinRequest{}, ErrNotFound
	}
	now := board.now()
	request := JoinRequest{Party: partyID, Name: name, Kills: kills, Created: now.Unix()}
	index := slices.IndexFunc(found.reserved, func(held poolReservation) bool { return sameName(held.member.Name, name) })
	// let in while its plugin wasn't polling, so it still says asking
	if status == RestoreAsking && index >= 0 {
		status = RestoreMember
	}
	switch status {
	case RestoreMember:
		if index < 0 {
			return JoinRequest{}, refuseAs(ErrGone, "Your place in this party wasn't kept")
		}
		request.Name = found.reserved[index].member.Name
		found.reserved = slices.Delete(found.reserved, index, index+1)
		board.addMember(found, PoolMember{Name: request.Name, Kills: kills}, token)
		request.Status = Accepted
	case RestoreAsking:
		if requestID == "" {
			requestID = board.freeID()
		} else if err := board.restoringID(requestID); err != nil {
			return JoinRequest{}, err
		}
		if found.full() {
			return JoinRequest{}, refuseAs(ErrFull, "This party just filled up")
		}
		request.ID, request.Status = requestID, Pending
		board.requests[requestID] = &joinRequest{request: request, token: token}
		board.requestByToken[token] = requestID
	default:
		return JoinRequest{}, invalid("only members and askers rejoin")
	}
	board.seen[token] = now
	return request, nil
}

func sameName(first, second string) bool {
	return strings.EqualFold(strings.ReplaceAll(first, "_", " "), strings.ReplaceAll(second, "_", " "))
}

func sameRoles(first, second []string) bool {
	first, second = slices.Clone(first), slices.Clone(second)
	slices.Sort(first)
	slices.Sort(second)
	return slices.Equal(first, second)
}
