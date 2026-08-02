package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestNewHomeSteps(t *testing.T) {
	t.Parallel()

	for path, step := range newHomeSteps {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("GET", "https://example.com"+path, nil)
			req = req.WithContext(ctxstore.WithUser(req.Context(), &db.User{TwitchLogin: "streamer"}))

			body := string((&API{}).newHome(req))
			for _, want := range []string{step.Title, "Step " + strconv.Itoa(step.Number) + " of 5"} {
				if !strings.Contains(body, want) {
					t.Fatalf("response for %s does not contain %q", path, want)
				}
			}
			if path == "/new_home/browser-properties" && !strings.Contains(body, "example.com/streamer") {
				t.Fatalf("response for %s does not contain overlay URL", path)
			}
		})
	}
}

func TestNewHomeRequiresUser(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "https://example.com/new_home", nil)
	body := string((&API{}).newHome(req))
	if !strings.Contains(body, "no user found") {
		t.Fatalf("response does not contain missing-user error: %s", body)
	}
}
