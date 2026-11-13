//go:build integration

package donations

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"app/db"

	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const pgImage = "pg_uuidv7:latest"

func startDB(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()

	pg, err := postgres.Run(ctx, pgImage,
		postgres.WithDatabase("test"), postgres.WithUsername("postgres"), postgres.WithPassword("postgres"),
		postgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("postgres container: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	connStr, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	database, err := db.New(ctx, &db.Config{ConnStr: connStr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := database.ApplyMigrations(ctx, db.Migrations, db.MigrationsDir, nil); err != nil {
		t.Fatalf("pg migrations: %v", err)
	}
	return database
}

// TestContainersStreamElementsAstro drives the real Astro source against the
// test account: it connects, subscribes, receives a mock tip and writes one
// donation row. SE_JWT and SE_ACCOUNT_ID come from cfg/donation_accounts.env.
func TestContainersStreamElementsAstro(t *testing.T) {
	jwt := os.Getenv("SE_JWT")
	channel := os.Getenv("SE_ACCOUNT_ID")
	if jwt == "" || channel == "" {
		t.Skip("SE_JWT and SE_ACCOUNT_ID are not set")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	database := startDB(t)
	userID, err := database.UpsertUser(ctx, &db.User{TwitchLogin: "donationtest", TwitchUserID: 424242})
	if err != nil {
		t.Fatal(err)
	}

	expiresAt, valid := ParseStreamElementsToken(jwt)
	if !valid {
		t.Fatal("SE_JWT is not a usable StreamElements token")
	}
	var expires *time.Time
	if !expiresAt.IsZero() {
		expires = &expiresAt
	}
	if err := database.UpsertDonationProvider(ctx, userID, db.ProviderStreamElements, jwt, nil, nil, expires); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager := NewManager(logger, database, NewStreamElements(logger))

	managerCtx, stopManager := context.WithCancel(ctx)
	defer stopManager()
	go manager.Run(managerCtx)

	spec := Spec{
		Credentials: Credentials{UserID: userID, AccessToken: jwt, ExpiresAt: expires},
		UpdatedAt:   time.Now(),
	}
	manager.Sync(ctx, map[Key]Spec{{UserID: userID, Provider: db.ProviderStreamElements}: spec})

	waitFor(t, 20*time.Second, "connection", func() bool {
		links, err := database.ConnectedDonationProviders(ctx, userID)
		return err == nil && links.State[db.ProviderStreamElements].Status == StatusConnected
	})
	waitFor(t, 20*time.Second, "rates", func() bool {
		_, ok := manager.rates.Rate("USD")
		return ok
	})

	fireMockTip(t, channel, jwt, "USD", 4.2, "containers probe", "ContainersProbe")

	var donation *db.Message
	waitFor(t, 30*time.Second, "donation row", func() bool {
		messages, err := database.GetMessageUpdates(ctx, userID, 0)
		if err != nil {
			return false
		}
		for _, message := range messages {
			if message.TwitchMessage.Event != nil && message.TwitchMessage.Event.Kind == db.EventKindDonation {
				donation = message
				return true
			}
		}
		return false
	})

	event := donation.TwitchMessage.Event
	if event.Provider != db.ProviderStreamElements || event.ProviderID == "" {
		t.Fatalf("event: %+v", event)
	}
	if event.AmountMinor != 420 || event.AmountText != "4.20 USD" || event.Currency != "USD" || event.USD != 4.2 {
		t.Fatalf("money: %+v", event)
	}
	if donation.TwitchMessage.Message != "containers probe" || donation.TwitchMessage.TwitchLogin != "ContainersProbe" {
		t.Fatalf("message: %+v", donation.TwitchMessage)
	}

	claimed, class, err := database.ClaimNextMsg(ctx, userID, [][]db.MsgClass{{db.MsgClassDonation}})
	if err != nil {
		t.Fatal(err)
	}
	if class != db.MsgClassDonation || claimed.ID != donation.ID {
		t.Fatalf("class: %s, id: %s", class, claimed.ID)
	}

	tokens, err := database.ListDonationProviderTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].LastEventAt == nil {
		t.Fatalf("the backfill watermark was not stored: %+v", tokens)
	}

	manager.Sync(ctx, map[Key]Spec{})
	if len(manager.live) != 0 {
		t.Fatalf("%d connections left after an empty sync", len(manager.live))
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func fireMockTip(t *testing.T, channel, jwt, currency string, amount float64, message, name string) {
	t.Helper()

	providerID, _ := jwtClaim(jwt, "provider_id")
	payload, _ := json.Marshal(map[string]any{
		"type":     "tip",
		"provider": "twitch",
		"isMock":   true,
		"flagged":  false,
		"data": map[string]any{
			"amount":      amount,
			"username":    strings.ToLower(name),
			"displayName": name,
			"message":     message,
			"currency":    currency,
			"providerId":  providerID,
		},
	})

	req, err := http.NewRequest(http.MethodPost,
		"https://api.streamelements.com/kappa/v2/activities/"+channel+"/mock",
		strings.NewReader(string(payload)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		t.Fatalf("mock tip: status %d, %s", resp.StatusCode, body)
	}
}
