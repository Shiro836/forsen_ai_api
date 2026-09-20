package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"
)

// redeemKey is what a redemption and the chat message it was made with have
// in common: who redeemed what with which text. Twitch gives them no shared id.
func redeemKey(viewerID int, rewardID, text string) string {
	text = strings.Map(func(r rune) rune {
		// U+E0000 is what chat clients append to get past the duplicate-message
		// check; it is unassigned, so the format-character class misses it.
		if unicode.Is(unicode.Cf, r) || r == 0xE0000 {
			return -1
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")

	sum := sha256.Sum256([]byte(strconv.Itoa(viewerID) + "\x00" + rewardID + "\x00" + text))
	return hex.EncodeToString(sum[:])
}
