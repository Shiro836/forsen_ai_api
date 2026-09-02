package seventv

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestV4FlagsRebuildTheV3Bitfield(t *testing.T) {
	var e v4Emote
	e.ID = "01J6KB8SBG00095HSJ1PN3XDPF"
	e.DefaultName = "forsenCream"
	e.Flags.PublicListed = true
	e.Flags.Animated = true

	got := e.toEmote()
	assert.Equal(t, "forsenCream", got.Name)
	assert.True(t, got.Listed)
	assert.True(t, got.Animated)
	assert.Equal(t, 0, got.Flags)

	e.Flags.NSFW = true
	e.Flags.Private = true
	e.Flags.DefaultZeroWidth = true
	e.Deleted = true

	got = e.toEmote()
	assert.Equal(t, FlagContentSexual|FlagPrivate|FlagZeroWidth, got.Flags)
	assert.True(t, got.Deleted)
}

func TestFrameCountReadsTheNamedFile(t *testing.T) {
	e := Emote{Host: Host{Files: []ImageFile{
		{Name: "1x.webp", FrameCount: 60},
		{Name: File4x, FrameCount: 60},
	}}}
	assert.Equal(t, 60, e.FrameCount(File4x))
	assert.Equal(t, 0, e.FrameCount("4x.avif"), "unlisted files report no frames")
}

func TestEmoteSetKeepsAliasAndBaseNameApart(t *testing.T) {
	set := emoteSetJSON{
		ID: "01HKQT8EWR000ESSWF3625XCS4",
		Emotes: []activeEmoteJSON{
			{ID: "a", Name: "7tvM", Flags: 0, Data: Emote{ID: "a", Name: "(7TV)"}},
			{ID: "b", Name: "RainTime", Flags: 1, Data: Emote{ID: "b", Name: "RainTime"}},
		},
	}.toSet()

	require.Len(t, set.Emotes, 2)
	assert.True(t, set.Emotes[0].Aliased())
	assert.Equal(t, "(7TV)", set.Emotes[0].BaseName)
	assert.False(t, set.Emotes[0].ZeroWidth)

	assert.False(t, set.Emotes[1].Aliased())
	assert.True(t, set.Emotes[1].ZeroWidth, "zero-width is the set entry's flag bit 0")
}
