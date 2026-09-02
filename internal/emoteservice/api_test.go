package emoteservice

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A filter naming nothing real would enqueue nothing and report success, which
// is the one outcome a resweep must not have.
func TestReclassifyRejectsUnknownClasses(t *testing.T) {
	service := &Service{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/reclassify", strings.NewReader(`{"classes":["nsfw"]}`))
	service.handleReclassify(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "known content classes")
}
