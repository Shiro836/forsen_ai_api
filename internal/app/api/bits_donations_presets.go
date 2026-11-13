package api

import "app/db"

type bdPreset struct {
	Name  string
	Lines map[db.EventLine]string
}

// A preset only fills the text fields; which one was picked is never stored.
var bdPresets = []bdPreset{
	{Name: "default", Lines: map[db.EventLine]string{
		db.EventLineSub:      db.EventLineSub.Default(),
		db.EventLineResub:    db.EventLineResub.Default(),
		db.EventLineGift:     db.EventLineGift.Default(),
		db.EventLineRaid:     db.EventLineRaid.Default(),
		db.EventLineStreak:   db.EventLineStreak.Default(),
		db.EventLineFollow:   db.EventLineFollow.Default(),
		db.EventLineDonation: db.EventLineDonation.Default(),
		db.EventLineBits:     db.EventLineBits.Default(),
	}},
	{Name: "cute", Lines: map[db.EventLine]string{
		db.EventLineSub:      "gura: {user} chan subscribed! Arigato, nya",
		db.EventLineResub:    "gura: {user} senpai stayed with us for {months} months! Sugoi",
		db.EventLineGift:     "gura: {user} sama gifted {count} tier {tier} subs! Kawaii",
		db.EventLineRaid:     "gura: Ara ara, {user} san is raiding with {viewers} nakama",
		db.EventLineStreak:   "gura: {user} kun came back {streak} streams in a row! Ganbatte",
		db.EventLineFollow:   "gura: {user} chan followed! Yoroshiku",
		db.EventLineDonation: "gura: {user} chan donated {amount} {currency}! Arigato, nya",
		db.EventLineBits:     "gura: {user} chan cheered {bits} bits! Arigato, nya",
	}},
	{Name: "gachi", Lines: map[db.EventLine]string{
		db.EventLineSub:      "billy: {user} joined the leather club. Welcome to the club, buddy",
		db.EventLineResub:    "billy: {user} has been a dungeon master for {months} months",
		db.EventLineGift:     "billy: {user} paid three hundred bucks for {count} tier {tier} new boys next door",
		db.EventLineRaid:     "billy: {user} and {viewers} leathermen are entering the dungeon",
		db.EventLineStreak:   "billy: {user} came to the gym {streak} streams in a row. Deep dark fantasies",
		db.EventLineFollow:   "billy: {user} wants to join the leather club",
		db.EventLineDonation: "billy: {user} put {amount} {currency} in the jar. Good boy",
		db.EventLineBits:     "billy: {user} cheered {bits} bits. Good boy",
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
