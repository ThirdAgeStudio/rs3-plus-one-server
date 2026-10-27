package board

type activity struct {
	maxPlayers int
	roles      []string
}

// keep in step with core/activities.lua in the plugin
var activities = map[string]activity{
	"vorago":  {maxPlayers: 10, roles: []string{"Base Tank", "Bomb Tank", "TL5", "DPS"}},
	"solak":   {maxPlayers: 7, roles: []string{"Base", "Mind Tank", "DPS"}},
	"croesus": {maxPlayers: 9, roles: []string{"Hunter", "Mining", "Woodcutting", "Fishing", "Rotter"}},
}
