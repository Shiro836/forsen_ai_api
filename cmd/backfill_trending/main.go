package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"app/cfg"
	"app/db"
	"app/internal/app/processor"
	"app/pkg/clickhouse"
	ttsprocessor "app/pkg/tts_processor"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type dayBucketKey struct {
	cardID uuid.UUID
	day    string
}

type dayBucketCounts struct {
	ai  int
	tts int
}

func main() {
	var (
		cfgPath string
		days    int
	)
	flag.StringVar(&cfgPath, "cfg-path", "cfg/cfg.yaml", "path to config file")
	flag.IntVar(&days, "days", 14, "number of days to backfill from ClickHouse")
	flag.Parse()

	if days <= 0 {
		log.Fatalf("-days must be positive, got %d", days)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		log.Fatalf("read %s: %v", cfgPath, err)
	}
	var conf cfg.Config
	if err := yaml.Unmarshal(raw, &conf); err != nil {
		log.Fatalf("parse %s: %v", cfgPath, err)
	}
	if conf.ClickHouse.Addr == "" {
		log.Fatal("clickhouse.addr is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pg, err := db.New(ctx, &conf.DB)
	if err != nil {
		log.Fatalf("init postgres: %v", err)
	}
	defer pg.Close()

	ch, err := clickhouse.Open(ctx, &conf.ClickHouse)
	if err != nil {
		log.Fatalf("open clickhouse: %v", err)
	}
	defer ch.Close()

	voiceCards, err := pg.GetPublicShortNamedCards(ctx)
	if err != nil {
		log.Fatalf("load voices: %v", err)
	}
	voiceByShort := make(map[string]uuid.UUID, len(voiceCards))
	shortByID := make(map[uuid.UUID]string, len(voiceCards))
	for _, vc := range voiceCards {
		key := strings.ToLower(strings.TrimSpace(vc.ShortCharName))
		voiceByShort[key] = vc.ID
		shortByID[vc.ID] = vc.ShortCharName
	}

	checkVoice := func(name string) bool {
		_, ok := voiceByShort[strings.ToLower(strings.TrimSpace(name))]
		return ok
	}
	noTag := func(string) bool { return false }

	since := time.Now().UTC().AddDate(0, 0, -days)
	rows, err := ch.Query(ctx, `
		SELECT handler, card_id, message, enqueued_at
		FROM messages FINAL
		WHERE enqueued_at >= {since:DateTime64(3)}
		  AND handler IN ('AI', 'TTS', 'BAJ AI', 'BAJ TTS')
	`, chgo.DateNamed("since", since, chgo.MilliSeconds))
	if err != nil {
		log.Fatalf("query clickhouse: %v", err)
	}
	defer rows.Close()

	buckets := make(map[dayBucketKey]*dayBucketCounts)
	add := func(cardID uuid.UUID, day string, ai, tts int) {
		k := dayBucketKey{cardID: cardID, day: day}
		c := buckets[k]
		if c == nil {
			c = &dayBucketCounts{}
			buckets[k] = c
		}
		c.ai += ai
		c.tts += tts
	}

	scanned := 0
	for rows.Next() {
		var (
			handler    string
			cardID     *uuid.UUID
			message    string
			enqueuedAt time.Time
		)
		if err := rows.Scan(&handler, &cardID, &message, &enqueuedAt); err != nil {
			log.Fatalf("scan clickhouse row: %v", err)
		}
		scanned++
		day := enqueuedAt.UTC().Format("2006-01-02")

		switch handler {
		case "AI":
			if cardID != nil && *cardID != uuid.Nil {
				add(*cardID, day, 1, 0)
			}
		case "TTS":
			if cardID != nil && *cardID != uuid.Nil {
				add(*cardID, day, 0, 1)
			}
		case "BAJ AI":
			voice := processor.DefaultUniversalVoice
			for _, tok := range ttsprocessor.Lex(message, checkVoice, noTag, noTag) {
				if tok.Kind == ttsprocessor.Voice {
					voice = strings.ToLower(strings.TrimSpace(tok.Value))
					break
				}
			}
			if id, ok := voiceByShort[voice]; ok {
				add(id, day, 1, 0)
			}
		case "BAJ TTS":
			tokens := ttsprocessor.Lex(message, checkVoice, noTag, noTag)
			actions := ttsprocessor.Actions(tokens, nil)
			seen := make(map[uuid.UUID]struct{})
			for _, a := range actions {
				if strings.TrimSpace(a.Text) == "" {
					continue
				}
				v := strings.ToLower(strings.TrimSpace(a.Voice))
				if v == "" {
					v = processor.DefaultUniversalVoice
				}
				if id, ok := voiceByShort[v]; ok {
					seen[id] = struct{}{}
				}
			}
			for id := range seen {
				add(id, day, 0, 1)
			}
		}
	}
	if err := rows.Err(); err != nil {
		log.Fatalf("iterate clickhouse rows: %v", err)
	}

	for k, c := range buckets {
		day, err := time.Parse("2006-01-02", k.day)
		if err != nil {
			log.Fatalf("parse day %s: %v", k.day, err)
		}
		if err := pg.UpsertCharCardDailyStats(ctx, k.cardID, day, c.ai, c.tts); err != nil {
			log.Fatalf("upsert daily stats (%s, %s): %v", k.cardID, k.day, err)
		}
	}

	fmt.Printf("scanned %d messages across last %d days, upserted %d daily buckets\n", scanned, days, len(buckets))

	updated, err := pg.GetPublicShortNamedCards(ctx)
	if err != nil {
		log.Fatalf("verify trending: %v", err)
	}
	sort.Slice(updated, func(i, j int) bool {
		return updated[i].TrendingScore > updated[j].TrendingScore
	})
	limit := min(10, len(updated))
	fmt.Println("top trending voices:")
	for i := range limit {
		fmt.Printf("  %2d. %-16s score=%.2f (all-time ai=%d tts=%d)\n",
			i+1, updated[i].ShortCharName, updated[i].TrendingScore, updated[i].Redeems, updated[i].TTSRedeems)
	}
}
