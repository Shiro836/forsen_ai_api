package db

import (
	"reflect"
	"testing"
)

func TestPlayOrder(t *testing.T) {
	split := [][]MsgClass{{MsgClassReward}, {MsgClassBits}, {MsgClassDonation}}

	cases := []struct {
		name   string
		stored [][]MsgClass
		want   [][]MsgClass
	}{
		{"nothing stored", nil, defaultPlayGroups},
		{"split and reordered", split, split},
		{"merged paid lanes", [][]MsgClass{{MsgClassReward}, {MsgClassBits, MsgClassDonation}}, [][]MsgClass{{MsgClassReward}, {MsgClassBits, MsgClassDonation}}},
		{"unpaid lane in a group", [][]MsgClass{{MsgClassReward, MsgClassBits}, {MsgClassDonation}}, defaultPlayGroups},
		{"lane missing", [][]MsgClass{{MsgClassReward}, {MsgClassBits}}, defaultPlayGroups},
		{"lane repeated", [][]MsgClass{{MsgClassReward}, {MsgClassBits}, {MsgClassDonation}, {MsgClassBits}}, defaultPlayGroups},
		{"chat ranked", [][]MsgClass{{MsgClassChat}, {MsgClassReward}, {MsgClassBits}, {MsgClassDonation}}, defaultPlayGroups},
		{"empty group", [][]MsgClass{{}, {MsgClassReward}, {MsgClassBits}, {MsgClassDonation}}, defaultPlayGroups},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := (&UserSettings{PlayGroups: tc.stored}).PlayOrder()
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("PlayOrder() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPlayOrderDoesNotAliasTheDefault(t *testing.T) {
	order := (&UserSettings{}).PlayOrder()
	order[0][0] = MsgClassChat
	if defaultPlayGroups[0][0] == MsgClassChat {
		t.Fatal("mutating a returned order changed the default")
	}
}
