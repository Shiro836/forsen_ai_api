package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestObsGuideSteps(t *testing.T) {
	t.Parallel()

	for path, step := range obsGuideSteps {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("GET", "https://example.com"+path, nil)
			req = req.WithContext(ctxstore.WithUser(req.Context(), &db.User{TwitchLogin: "streamer"}))

			body := string((&API{}).obsGuide(req))
			for _, want := range []string{step.Title, "Step " + strconv.Itoa(step.Number) + " of 5"} {
				if !strings.Contains(body, want) {
					t.Fatalf("response for %s does not contain %q", path, want)
				}
			}
			if path == "/guide/browser-properties" && !strings.Contains(body, "example.com/streamer") {
				t.Fatalf("response for %s does not contain overlay URL", path)
			}
		})
	}
}

func TestAudioGuideSteps(t *testing.T) {
	t.Parallel()

	for path, step := range audioGuideSteps {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("GET", "https://example.com"+path, nil)

			body := string((&API{}).audioGuide(req))
			for _, want := range []string{step.Title, "Step " + strconv.Itoa(step.Number) + " of 6"} {
				if !strings.Contains(body, want) {
					t.Fatalf("response for %s does not contain %q", path, want)
				}
			}
		})
	}
}

func TestLegacyHome(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "https://example.com/guide/legacy", nil)
	req = req.WithContext(ctxstore.WithUser(req.Context(), &db.User{TwitchLogin: "streamer"}))

	body := string((&API{}).legacyHome(req))
	for _, want := range []string{"Quickstart", "obs_script.lua", "example.com/streamer"} {
		if !strings.Contains(body, want) {
			t.Fatalf("legacy home does not contain %q", want)
		}
	}
}

func TestObsGuideRequiresUser(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "https://example.com/", nil)
	body := string((&API{}).obsGuide(req))
	if !strings.Contains(body, "no user found") {
		t.Fatalf("response does not contain missing-user error: %s", body)
	}
}
