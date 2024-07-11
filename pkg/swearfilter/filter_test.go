package swearfilter_test

import (
	"app/pkg/swearfilter"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSwearFilter(t *testing.T) {
	assert := require.New(t)

	filters := []string{"lulE", "moron"}

	swearFilter := swearfilter.NewSwearFilter(false, filters...)

	msg := "forsen LULE xd moroN LULA oaosdfjn"

	tripped, err := swearFilter.Check(msg)
	assert.NoError(err)

	assert.Len(tripped, 2)
}
