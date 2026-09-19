package api

import "app/db"

type bdPreset struct {
	Name  string
	Lines map[db.EventLine]string
}

// A preset only fills the text fields; which one was picked is never stored.
var bdPresets = []bdPreset{
	{Name: "default", Lines: map[db.EventLine]string{
		db.EventLineSub:    db.EventLineSub.Default(),
		db.EventLineResub:  db.EventLineResub.Default(),
		db.EventLineGift:   db.EventLineGift.Default(),
		db.EventLineRaid:   db.EventLineRaid.Default(),
		db.EventLineStreak: db.EventLineStreak.Default(),
	}},
	{Name: "cute", Lines: map[db.EventLine]string{
		db.EventLineSub:    "{user} chan subscribed! Arigato, nya",
		db.EventLineResub:  "{user} senpai stayed with us for {months} months! Sugoi",
		db.EventLineGift:   "{user} sama gifted {count} subs! Kawaii",
		db.EventLineRaid:   "Ara ara, {user} san is raiding with {viewers} nakama",
		db.EventLineStreak: "{user} kun came back {streak} streams in a row! Ganbatte",
	}},
	{Name: "gachi", Lines: map[db.EventLine]string{
		db.EventLineSub:    "{user} joined the leather club. Welcome to the club, buddy",
		db.EventLineResub:  "{user} has been a dungeon master for {months} months",
		db.EventLineGift:   "{user} paid three hundred bucks for {count} new boys next door",
		db.EventLineRaid:   "{user} and {viewers} leathermen are entering the dungeon",
		db.EventLineStreak: "{user} came to the gym {streak} streams in a row. Deep dark fantasies",
	}},
	{Name: "forsen", Lines: map[db.EventLine]string{
		db.EventLineSub:    "{user} is now a baj. I see",
		db.EventLineResub:  "{user}, {months} months a baj. God gamer",
		db.EventLineGift:   "{user} gifted {count} subs. Snus for the bajs",
		db.EventLineRaid:   "{viewers} stream snipers from {user} are here",
		db.EventLineStreak: "{user} has not missed {streak} streams. Go outside, baj",
	}},
}

func bdPresetNames() []string {
	names := make([]string, len(bdPresets))
	for i, preset := range bdPresets {
		names[i] = preset.Name
	}
	return names
}

func bdPresetByName(name string) *bdPreset {
	for i := range bdPresets {
		if bdPresets[i].Name == name {
			return &bdPresets[i]
		}
	}
	return nil
}
