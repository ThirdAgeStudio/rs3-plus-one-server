//go:build dev

package api

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/ThirdAgeStudio/rs3-plus-one-server/internal/board"
)

type fakes struct {
	server  *handler
	mu      sync.Mutex
	testers map[string]*tester
	named   map[string]int
}

type tester struct {
	hosts      map[string]string
	applicants map[string]string
	leaders    map[string]string
	askers     map[string]string
	posted     int
	changes    int
}

const (
	hostName      = "Host"
	applicantName = "Applicant"
	leaderName    = "Leader"
	askerName     = "Asker"
)

const DevBuild = true

func registerDev(server *handler, mux *http.ServeMux) {
	server.board.TrackPollers()
	fake := &fakes{server: server, testers: map[string]*tester{}, named: map[string]int{}}
	mux.HandleFunc("POST /dev/apply", fake.apply)
	mux.HandleFunc("POST /dev/host", fake.host)
	mux.HandleFunc("POST /dev/answer", fake.answer)
	mux.HandleFunc("POST /dev/leave", fake.leave)
	mux.HandleFunc("POST /dev/quiet", fake.quiet)
	mux.HandleFunc("POST /dev/clear", fake.clear)
	mux.HandleFunc("POST /dev/clear/applicants", fake.clearApplicants)
	mux.HandleFunc("POST /dev/pool", fake.pool)
	mux.HandleFunc("POST /dev/pool/ask", fake.poolAsk)
	mux.HandleFunc("POST /dev/board/fill", fake.fill)
	mux.HandleFunc("POST /dev/board/change", fake.change)
}

type devClient struct {
	Client string `json:"client"`
}

func (fake *fakes) tester(writer http.ResponseWriter, client string) (*tester, bool) {
	if !board.ValidClient(client) {
		fake.server.write(writer, http.StatusBadRequest, errorAnswer{Error: "dev requests need the plugin's client id"})
		return nil, false
	}
	found, ok := fake.testers[client]
	if !ok {
		found = &tester{hosts: map[string]string{}, applicants: map[string]string{},
			leaders: map[string]string{}, askers: map[string]string{}}
		fake.testers[client] = found
	}
	return found, true
}

func (fake *fakes) name(kind string) (string, int) {
	fake.named[kind]++
	number := (fake.named[kind]-1)%99 + 1
	return fmt.Sprintf("%s %d", kind, number), number
}

func tokensOf(group map[string]string) []string {
	tokens := []string{}
	for _, token := range group {
		tokens = append(tokens, token)
	}
	return tokens
}

type devApplyRequest struct {
	devClient
	Role  string `json:"role"`
	Count int    `json:"count"`
}

func (fake *fakes) apply(writer http.ResponseWriter, request *http.Request) {
	var body devApplyRequest
	if !fake.server.read(writer, request, &body) {
		return
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	tester, ok := fake.tester(writer, body.Client)
	if !ok {
		return
	}
	listing, ok := fake.server.board.HostedBy(body.Client)
	if !ok {
		fake.server.fail(writer, board.ErrNotFound)
		return
	}
	made := []board.Application{}
	for index := range max(1, body.Count) {
		role := body.Role
		if role == "" && len(listing.Needs) > 0 {
			role = listing.Needs[index%len(listing.Needs)]
		}
		roles := []string{}
		if role != "" {
			roles = append(roles, role)
		}
		name, number := fake.name(applicantName)
		fields := board.Application{Applicant: name, Roles: roles, Kills: 40 + 37*number, Combat: 138}
		application, token, err := fake.server.board.Apply(listing.ID, fields)
		if err != nil {
			fake.server.fail(writer, err)
			return
		}
		fake.server.board.Keep(token, true)
		tester.applicants[application.ID] = token
		made = append(made, application)
	}
	fake.server.write(writer, http.StatusOK, made)
}

type devHostRequest struct {
	devClient
	board.Listing
}

func (fake *fakes) host(writer http.ResponseWriter, request *http.Request) {
	body := devHostRequest{Listing: board.Listing{Activity: "vorago", Size: 5, Filled: 3,
		HostRoles: []string{"Base Tank", "DPS", "DPS"}, Needs: []string{"Bomb Tank", "TL5"}, World: 119}}
	if !fake.server.read(writer, request, &body) {
		return
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	tester, ok := fake.tester(writer, body.Client)
	if !ok {
		return
	}
	fields := body.Listing
	fields.Host, _ = fake.name(hostName)
	listing, token, err := fake.server.board.Post(fields)
	if err != nil {
		fake.server.fail(writer, err)
		return
	}
	fake.server.board.Keep(token, true)
	tester.hosts[listing.ID] = token
	fake.server.write(writer, http.StatusOK, listing)
}

type devAnswerRequest struct {
	devClient
	Accept bool `json:"accept"`
}

func (fake *fakes) answer(writer http.ResponseWriter, request *http.Request) {
	var body devAnswerRequest
	if !fake.server.read(writer, request, &body) {
		return
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	tester, ok := fake.tester(writer, body.Client)
	if !ok {
		return
	}
	for _, inPool := range fake.server.board.Poll(board.PollRequest{Tokens: tokensOf(tester.leaders)}).InPool {
		for _, asked := range inPool.Requests {
			fake.server.board.AnswerRequest(asked.ID, inPool.Token, body.Accept)
		}
	}
	answered := []board.Application{}
	for _, hosting := range fake.server.board.Poll(board.PollRequest{Tokens: tokensOf(tester.hosts)}).Hosting {
		for _, waiting := range hosting.Applications {
			if waiting.Status != board.Pending {
				continue
			}
			if application, err := fake.server.board.Decide(waiting.ID, hosting.Token, body.Accept); err == nil {
				answered = append(answered, application)
			}
		}
	}
	fake.server.write(writer, http.StatusOK, answered)
}

func (fake *fakes) leave(writer http.ResponseWriter, request *http.Request) {
	var body devClient
	if !fake.server.read(writer, request, &body) {
		return
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	tester, ok := fake.tester(writer, body.Client)
	if !ok {
		return
	}
	listing, ok := fake.server.board.HostedBy(body.Client)
	if !ok {
		fake.server.fail(writer, board.ErrNotFound)
		return
	}
	for _, applied := range fake.server.board.Poll(board.PollRequest{Tokens: tokensOf(tester.applicants)}).Applied {
		if applied.Application.Listing != listing.ID {
			continue
		}
		if err := fake.server.board.Withdraw(applied.Application.ID, applied.Token); err != nil {
			fake.server.fail(writer, err)
			return
		}
		delete(tester.applicants, applied.Application.ID)
		fake.server.write(writer, http.StatusOK, applied.Application)
		return
	}
	fake.server.fail(writer, board.ErrNotFound)
}

func (fake *fakes) quiet(writer http.ResponseWriter, request *http.Request) {
	var body devClient
	if !fake.server.read(writer, request, &body) {
		return
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	tester, ok := fake.tester(writer, body.Client)
	if !ok {
		return
	}
	for _, group := range []map[string]string{tester.hosts, tester.applicants, tester.leaders, tester.askers} {
		for id, token := range group {
			fake.server.board.Keep(token, false)
			delete(group, id)
		}
	}
	fake.server.write(writer, http.StatusOK, struct{}{})
}

func (fake *fakes) clear(writer http.ResponseWriter, request *http.Request) {
	var body devClient
	if !fake.server.read(writer, request, &body) {
		return
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	tester, ok := fake.tester(writer, body.Client)
	if !ok {
		return
	}
	for id, token := range tester.applicants {
		fake.server.board.Keep(token, false)
		fake.server.board.Withdraw(id, token)
	}
	for id, token := range tester.hosts {
		fake.server.board.Keep(token, false)
		fake.server.board.Close(id, token)
	}
	for _, token := range append(tokensOf(tester.leaders), tokensOf(tester.askers)...) {
		fake.server.board.Keep(token, false)
		fake.server.board.LeavePool(token)
	}
	delete(fake.testers, body.Client)
	fake.server.write(writer, http.StatusOK, struct{}{})
}

type devCleared struct {
	Cleared int `json:"cleared"`
}

func (fake *fakes) clearApplicants(writer http.ResponseWriter, request *http.Request) {
	var body devClient
	if !fake.server.read(writer, request, &body) {
		return
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	tester, ok := fake.tester(writer, body.Client)
	if !ok {
		return
	}
	cleared := 0
	for id, token := range tester.applicants {
		fake.server.board.Keep(token, false)
		if fake.server.board.Withdraw(id, token) == nil {
			cleared++
		}
		delete(tester.applicants, id)
	}
	fake.server.write(writer, http.StatusOK, devCleared{Cleared: cleared})
}

type devPoolRequest struct {
	devClient
	Boss  int `json:"boss"`
	Count int `json:"count"`
}

func (fake *fakes) pool(writer http.ResponseWriter, request *http.Request) {
	var body devPoolRequest
	if !fake.server.read(writer, request, &body) {
		return
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	tester, ok := fake.tester(writer, body.Client)
	if !ok {
		return
	}
	made := []board.PoolParty{}
	for index := range max(1, body.Count) {
		boss := body.Boss
		if index%3 == 2 {
			boss = boss%60 + 1
		}
		kills := board.Kills{Done: (index * 3) % 10, Total: 10 + index}
		name, _ := fake.name(leaderName)
		party, token, err := fake.server.board.StartPoolParty(board.NewParty{
			Name: name, Kills: kills, Boss: boss, World: 21 + index*38%200, Size: 3 + index%2, Open: index%2 == 0,
		})
		if err != nil {
			fake.server.fail(writer, err)
			return
		}
		fake.server.board.Keep(token, true)
		tester.leaders[party.ID] = token
		made = append(made, party)
	}
	fake.server.write(writer, http.StatusOK, made)
}

type devPoolAskRequest struct {
	devClient
	Boss int `json:"boss"`
}

func (fake *fakes) poolAsk(writer http.ResponseWriter, request *http.Request) {
	var body devPoolAskRequest
	if !fake.server.read(writer, request, &body) {
		return
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	tester, ok := fake.tester(writer, body.Client)
	if !ok {
		return
	}
	party, ok := fake.server.board.PartyOf(body.Client)
	if !ok {
		fake.server.fail(writer, board.ErrNotFound)
		return
	}
	name, _ := fake.name(askerName)
	asked, token, err := fake.server.board.RequestToJoin(party.ID, name, board.Kills{Done: 1, Total: 12}, max(1, body.Boss))
	if err != nil {
		fake.server.fail(writer, err)
		return
	}
	fake.server.board.Keep(token, true)
	tester.askers[asked.ID] = token
	fake.server.write(writer, http.StatusOK, asked)
}
