package runs

import (
	"maps"
	"slices"

	"github.com/noamsto/houston/crewview"
)

// logWorkers synthesizes crew's overview rows for the branches of ids whose
// crew include accepts, from the status fold of the bus log. It stands in for
// a view that omits a crew whose dispatcher is live on another bus; every row
// and item is local-origin, ages are against generatedAt, and items carry no
// Action, since houston has nothing of crew's to run.
func logWorkers(ids map[string]crewIdentity, generatedAt int64, include func(crew string) bool) ([]crewview.Worker, []crewview.Item) {
	var workers []crewview.Worker
	var items []crewview.Item
	ageOf := func(ms int64) int64 { return generatedAt - ms/1000 }

	for _, branch := range slices.Sorted(maps.Keys(ids)) {
		id := ids[branch]
		// crew lists a worker from its first status and drops a reaped one.
		if id.crew == "" || id.statusTS == 0 || id.state == "reaped" || !include(id.crew) {
			continue
		}
		w := crewview.Worker{
			Crew: id.crew, Branch: branch, Origin: crewview.OriginLocal, State: id.state,
			Name: optional(id.codename), Detail: optional(id.detail), Tier: optional(id.tier),
			Engine: optional(id.engine), Model: optional(id.model), Title: optional(id.title), PRURL: optional(id.prURL),
		}
		var age int64
		if id.statusTS != 0 {
			age = ageOf(id.statusTS)
			w.AgeSec = &age
		}
		if id.blockedSince != 0 {
			w.BlockedSince = &id.blockedSince
		}
		workers = append(workers, w)

		item := func(kind, message string, sinceMS int64) crewview.Item {
			it := crewview.Item{ID: kind + ":" + id.crew + ":" + branch, Crew: id.crew, Branch: branch, Origin: crewview.OriginLocal, Kind: kind, Message: message}
			if sinceMS != 0 {
				it.AgeSec = ageOf(sinceMS)
			}
			return it
		}
		if id.state == "blocked" {
			items = append(items, item("blocked", id.detail, id.blockedSince))
		}
		if id.watchdog != (watchdogItem{}) {
			items = append(items, item(id.watchdog.kind, id.watchdog.message, id.statusTS))
		}
	}
	return workers, items
}

// optional is the nullable string crew's JSON uses for an absent field.
func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
