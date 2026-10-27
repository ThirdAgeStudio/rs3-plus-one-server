package board

import (
	"slices"
	"strings"
)

const (
	Pending  = "pending"
	Accepted = "accepted"
	Declined = "declined"

	maxPending = 20
	maxCombat = 152
	maxKills  = 1_000_000
)

type Application struct {
	ID        string   `json:"id"`
	Listing   string   `json:"listing"`
	Applicant string   `json:"applicant"`
	Roles     []string `json:"roles"`
	Kills     int      `json:"kills"`
	Combat    int      `json:"combat"`
	Status    string   `json:"status"`
	Created   int64    `json:"created"`
}

type Member struct {
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}

type application struct {
	application Application
	token       string
}

func cleanApplication(fields Application, listing Listing) (Application, error) {
	cleaned := Application{
		Roles:  []string{},
		Kills:  fields.Kills,
		Combat: fields.Combat,
	}
	open := slices.Clone(listing.Needs)
	for _, role := range fields.Roles {
		role = strings.TrimSpace(role)
		index := slices.Index(open, role)
		if index < 0 {
			return Application{}, invalid("a role the listing doesn't need")
		}
		open = slices.Delete(open, index, index+1)
		cleaned.Roles = append(cleaned.Roles, role)
	}
	var err error
	if cleaned.Applicant, err = CleanName(fields.Applicant); err != nil {
		return Application{}, err
	}
	if len(listing.Needs) > 0 && len(cleaned.Roles) == 0 {
		return Application{}, invalid("pick at least one role")
	}
	return cleaned, checkStats(cleaned)
}

func checkStats(fields Application) error {
	switch {
	case fields.Kills < 0 || fields.Kills > maxKills:
		return invalid("kill count out of range")
	case fields.Combat < 0 || fields.Combat > maxCombat:
		return invalid("combat level out of range")
	}
	return nil
}

func (board *Board) Apply(listingID string, fields Application) (Application, string, error) {
	board.mu.Lock()
	defer board.mu.Unlock()
	token := randomHex(16)
	applied, err := board.apply(listingID, fields, board.freeID(), token)
	return applied, token, err
}

func (board *Board) apply(listingID string, fields Application, id, token string) (Application, error) {
	host, ok := board.listings[listingID]
	if !ok {
		return Application{}, refuseAs(ErrNotFound, "This listing has closed")
	}
	if host.listing.Filled >= host.listing.Size {
		return Application{}, refuseAs(ErrFull, "This party just filled up")
	}
	if board.pendingFor(host) >= maxPending {
		return Application{}, refuseAs(ErrTooMany, "This party has too many applications waiting, try again soon")
	}
	cleaned, err := cleanApplication(fields, host.listing)
	if err != nil {
		return Application{}, err
	}
	if board.waitingFor(cleaned.Applicant) >= maxWaitingPerName {
		return Application{}, errTooManyWaiting
	}
	cleaned.ID, cleaned.Listing, cleaned.Status = id, listingID, Pending
	cleaned = board.addApplication(host, cleaned, token)
	board.updateBusy(host)
	return cleaned, nil
}

func (board *Board) addApplication(host *entry, applied Application, token string) Application {
	now := board.now()
	applied.Created = now.Unix()
	board.applications[applied.ID] = &application{application: applied, token: token}
	board.appByToken[token] = applied.ID
	board.seen[token] = now
	host.applications = append(host.applications, applied.ID)
	return applied
}

func (board *Board) pendingFor(host *entry) int {
	count := 0
	for _, id := range host.applications {
		if board.applications[id].application.Status == Pending {
			count++
		}
	}
	return count
}

func (board *Board) Decide(applicationID, hostToken string, accept bool) (Application, error) {
	board.mu.Lock()
	defer board.mu.Unlock()
	found, ok := board.applications[applicationID]
	if !ok {
		return Application{}, ErrNotFound
	}
	host := board.listings[found.application.Listing]
	if host.token != hostToken {
		return Application{}, ErrWrongKey
	}
	if found.application.Status != Pending {
		return Application{}, ErrNotPending
	}
	if !accept {
		found.application.Status = Declined
		board.updateBusy(host)
		return found.application, nil
	}
	if host.listing.Filled >= host.listing.Size {
		return Application{}, refuseAs(ErrFull, "Your party is already full")
	}
	needs := slices.Clone(host.listing.Needs)
	for _, role := range found.application.Roles {
		index := slices.Index(needs, role)
		if index < 0 {
			return Application{}, refuseAs(ErrRoleTaken, "You've already accepted someone for that role")
		}
		needs = slices.Delete(needs, index, index+1)
	}
	found.application.Status = Accepted
	host.listing.Filled++
	host.listing.Needs = needs
	host.listing.Members = append(host.listing.Members, Member{Name: found.application.Applicant, Roles: found.application.Roles})
	board.touch(host)
	board.updateBusy(host)
	return found.application, nil
}

func (board *Board) Withdraw(applicationID, token string) error {
	board.mu.Lock()
	defer board.mu.Unlock()
	found, ok := board.applications[applicationID]
	if !ok {
		return ErrNotFound
	}
	if found.token != token {
		return ErrWrongKey
	}
	board.withdraw(applicationID)
	return nil
}

func (board *Board) withdraw(applicationID string) {
	found := board.applications[applicationID]
	if found.application.Status == Accepted {
		host := board.listings[found.application.Listing]
		host.listing.Filled--
		host.listing.Needs = append(host.listing.Needs, found.application.Roles...)
		// two members can share a name, so the roles pick out the one leaving
		index := slices.IndexFunc(host.listing.Members, func(member Member) bool {
			return member.Name == found.application.Applicant && slices.Equal(member.Roles, found.application.Roles)
		})
		if index >= 0 {
			host.listing.Members = slices.Delete(host.listing.Members, index, index+1)
		}
		board.touch(host)
	}
	board.dropApplication(applicationID)
}

func (board *Board) dropApplication(id string) {
	found := board.applications[id]
	delete(board.appByToken, found.token)
	delete(board.seen, found.token)
	delete(board.kept, found.token)
	delete(board.applications, id)
	if host, ok := board.listings[found.application.Listing]; ok {
		host.applications = slices.DeleteFunc(host.applications, func(other string) bool { return other == id })
		board.updateBusy(host)
	}
}

func (board *Board) updateBusy(host *entry) {
	if busy := board.pendingFor(host) >= maxPending; busy != host.listing.Busy {
		host.listing.Busy = busy
		board.touch(host)
	}
}

type HostView struct {
	Token        string        `json:"token"`
	Listing      Listing       `json:"listing"`
	Applications []Application `json:"applications"`
}

type ApplicantView struct {
	Token       string      `json:"token"`
	Application Application `json:"application"`
	Party       *Listing    `json:"party,omitempty"`
}

func (board *Board) hostView(token string, host *entry) HostView {
	view := HostView{Token: token, Listing: host.listing.clone(), Applications: []Application{}}
	for _, id := range host.applications {
		view.Applications = append(view.Applications, board.applications[id].application)
	}
	return view
}

func (board *Board) applicantView(token string, found *application) ApplicantView {
	view := ApplicantView{Token: token, Application: found.application}
	if found.application.Status == Accepted {
		party := board.listings[found.application.Listing].listing.clone()
		view.Party = &party
	}
	return view
}
