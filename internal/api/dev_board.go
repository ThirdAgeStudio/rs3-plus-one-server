//go:build dev

package api

import (
	"net/http"

	"github.com/ThirdAgeStudio/rs3-plus-one-server/internal/board"
)

var fakeListings = []board.Listing{
	{Activity: "vorago", Size: 5, Filled: 3, HostRoles: []string{"Base Tank", "DPS", "DPS"}, Needs: []string{"Bomb Tank", "DPS"}, World: 119, Tags: []string{"lootshare", "teacher"}},
	{Activity: "solak", Size: 4, Filled: 1, HostRoles: []string{"DPS"}, Needs: []string{"Base", "DPS", "DPS"}, World: 97, Tags: []string{"learner"}},
	{Activity: "croesus", Size: 4, Filled: 2, HostRoles: []string{"Woodcutting", "Hunter"}, Needs: []string{"Fishing", "Mining"}, World: 50, Tags: []string{"lootshare", "chill"}},
	{Activity: "vorago", Size: 7, Filled: 6, HostRoles: []string{"Base Tank", "Bomb Tank", "TL5", "DPS", "DPS", "DPS"}, Needs: []string{"DPS"}, World: 21},
	{Activity: "solak", Size: 3, Filled: 1, HostRoles: []string{"Base"}, Needs: []string{"Mind Tank", "DPS"}, World: 44, Tags: []string{"combat_achievement"}},
	{Activity: "croesus", Size: 5, Filled: 1, HostRoles: []string{"Rotter"}, Needs: []string{"Hunter", "Mining", "Woodcutting", "Fishing"}, World: 77, Tags: []string{"reaper_task_only"}},
	{Activity: "vorago", Size: 4, Filled: 2, HostRoles: []string{"Base Tank", "DPS"}, Needs: []string{"Bomb Tank", "TL5"}, World: 140, Tags: []string{"chill", "learner"}},
	{Activity: "solak", Size: 7, Filled: 2, HostRoles: []string{"Base", "DPS"}, Needs: []string{"Mind Tank", "DPS", "DPS", "DPS", "DPS"}, World: 2, Tags: []string{"teacher"}},
}

func (fake *fakes) postFake(tester *tester) (board.Listing, error) {
	fields := fakeListings[tester.posted%len(fakeListings)]
	tester.posted++
	fields.Host, _ = fake.name(hostName)
	listing, token, err := fake.server.board.Post(fields)
	if err != nil {
		return board.Listing{}, err
	}
	fake.server.board.Keep(token, true)
	tester.hosts[listing.ID] = token
	return listing, nil
}

type devFillRequest struct {
	devClient
	Count int `json:"count"`
}

func (fake *fakes) fill(writer http.ResponseWriter, request *http.Request) {
	body := devFillRequest{Count: len(fakeListings)}
	if !fake.server.read(writer, request, &body) {
		return
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	tester, ok := fake.tester(writer, body.Client)
	if !ok {
		return
	}
	made := []board.Listing{}
	for range max(1, body.Count) {
		listing, err := fake.postFake(tester)
		if err != nil {
			fake.server.fail(writer, err)
			return
		}
		made = append(made, listing)
	}
	fake.server.write(writer, http.StatusOK, made)
}

type devChange struct {
	Change  string        `json:"change"`
	Listing board.Listing `json:"listing"`
}

func (fake *fakes) change(writer http.ResponseWriter, request *http.Request) {
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
	step := tester.changes % 3
	tester.changes++
	if step == 0 {
		listing, err := fake.postFake(tester)
		if err != nil {
			fake.server.fail(writer, err)
			return
		}
		fake.server.write(writer, http.StatusOK, devChange{Change: "posted", Listing: listing})
		return
	}
	oldest, found := board.Listing{}, false
	for _, listing := range fake.server.board.Listings() {
		open := listing.Filled < listing.Size && len(listing.Needs) > 0
		if _, isFake := tester.hosts[listing.ID]; isFake && open && (!found || listing.Created < oldest.Created) {
			oldest, found = listing, true
		}
	}
	if !found {
		fake.server.fail(writer, board.ErrNotFound)
		return
	}
	hostToken := tester.hosts[oldest.ID]
	if step == 2 {
		if err := fake.server.board.Close(oldest.ID, hostToken); err != nil {
			fake.server.fail(writer, err)
			return
		}
		delete(tester.hosts, oldest.ID)
		fake.server.write(writer, http.StatusOK, devChange{Change: "closed", Listing: oldest})
		return
	}
	name, number := fake.name(applicantName)
	fields := board.Application{Applicant: name, Roles: oldest.Needs[:1], Kills: 40 + 37*number, Combat: 138}
	applied, token, err := fake.server.board.Apply(oldest.ID, fields)
	if err == nil {
		fake.server.board.Keep(token, true)
		tester.applicants[applied.ID] = token
		_, err = fake.server.board.Decide(applied.ID, hostToken, true)
	}
	if err != nil {
		fake.server.fail(writer, err)
		return
	}
	fake.server.write(writer, http.StatusOK, devChange{Change: "joined", Listing: oldest})
}
