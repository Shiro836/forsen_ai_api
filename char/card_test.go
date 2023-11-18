package char_test

import (
	"app/char"
	"testing"

	"github.com/kylelemons/godebug/pretty"
	"github.com/stretchr/testify/assert"

	_ "embed"
)

//go:embed test_cards/forsen.png
var pngCard []byte

func TestFromSillyTavernCard(t *testing.T) {
	assert := assert.New(t)

	card, err := char.FromPngSillyTavernCard(pngCard)
	assert.NoError(err)
	pretty.Print(card)
}
