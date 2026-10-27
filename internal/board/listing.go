package board

import (
	"fmt"
	"slices"
)

// no free text: one player's words to strangers would go round the game's chat filter and reporting
type Listing struct {
	ID        string   `json:"id"`
	Host      string   `json:"host"`
	Activity  string   `json:"activity"`
	Size      int      `json:"size"`
	Filled    int      `json:"filled"`
	Needs     []string `json:"needs"`
	HostRoles []string `json:"hostRoles"`
	World     int      `json:"world"`
	Tags      []string `json:"tags"`
	Members   []Member `json:"members"`
	Created   int64    `json:"created"`
	Busy      bool     `json:"busy,omitempty"`
}

// the API can't read the player's world (yet), so the host types it
const maxWorld = 259

var tags = []string{"lootshare", "learner", "teacher", "chill", "combat_achievement", "reaper_task_only"}

func clean(fields Listing) (Listing, error) {
	listing, known, err := cleanShape(fields)
	if err != nil {
		return Listing{}, err
	}
	switch {
	case listing.Filled < 1 || listing.Filled >= listing.Size:
		return Listing{}, refuse("Add a role you need")
	case len(known.roles) > 0 && len(listing.HostRoles) == 0:
		return Listing{}, refuse("Pick the role you have")
	case len(known.roles) > 0 && len(listing.Needs) < listing.Size-listing.Filled:
		return Listing{}, refuse("Every open spot needs at least one role")
	}
	return listing, nil
}

func cleanShape(fields Listing) (Listing, activity, error) {
	listing := Listing{
		Activity:  fields.Activity,
		Size:      fields.Size,
		Filled:    fields.Filled,
		World:     fields.World,
		Needs:     []string{},
		HostRoles: []string{},
		Tags:      []string{},
		Members:   []Member{},
	}
	var err error
	if listing.Host, err = CleanName(fields.Host); err != nil {
		return Listing{}, activity{}, err
	}
	known, ok := activities[listing.Activity]
	switch {
	case !ok:
		return Listing{}, activity{}, refuse("Pick an activity")
	case listing.Size < 2 || listing.Size > known.maxPlayers:
		return Listing{}, activity{}, refuse(fmt.Sprintf("Team size must be 2 to %d", known.maxPlayers))
	case listing.World < 1 || listing.World > maxWorld:
		return Listing{}, activity{}, refuse("Enter your world (1 to 259)")
	}
	if listing.Needs, err = knownRoles(fields.Needs, known); err != nil {
		return Listing{}, activity{}, err
	}
	if listing.HostRoles, err = knownRoles(fields.HostRoles, known); err != nil {
		return Listing{}, activity{}, err
	}
	for _, tag := range tags {
		if slices.Contains(fields.Tags, tag) {
			listing.Tags = append(listing.Tags, tag)
		}
	}
	return listing, known, nil
}

// the api encodes after the board's lock is released, so everything leaving the board is a copy
func (listing Listing) clone() Listing {
	listing.Needs = slices.Clone(listing.Needs)
	listing.HostRoles = slices.Clone(listing.HostRoles)
	listing.Tags = slices.Clone(listing.Tags)
	listing.Members = slices.Clone(listing.Members)
	for index := range listing.Members {
		listing.Members[index].Roles = slices.Clone(listing.Members[index].Roles)
	}
	return listing
}

func knownRoles(roles []string, known activity) ([]string, error) {
	cleaned := []string{}
	for _, role := range roles {
		if !slices.Contains(known.roles, role) {
			return nil, refuse("Pick roles from this activity")
		}
		cleaned = append(cleaned, role)
	}
	return cleaned, nil
}
