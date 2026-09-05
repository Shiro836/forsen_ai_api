//go:build integration

// The labeled corpus for the emote classifier, the counterpart to
// pkg/llmfilter's. Every case in testdata/corpus.json is classified against the
// live vision model from its stored original, compared to its expected classes,
// and scored per class. It runs on real emotes rather than synthetic ones
// because the failures worth catching are the ones the catalogue actually
// contains.
package emoteservice

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"app/pkg/oai"
	"app/pkg/s3client"

	"gopkg.in/yaml.v3"

	"github.com/stretchr/testify/require"
)

// corpusCase is one labeled emote. expected_classes is what Classify must
// return, which is the vision taxonomy only: the flashing class never appears,
// since the photometric measure owns it and Classify strips it. An advisory case
// is scored and printed but cannot fail the run — its imagery is ambiguous
// enough that the model's reading of it moves between runs, and pinning it would
// make the corpus flap instead of catching regressions.
type corpusCase struct {
	Provider        string   `json:"provider"`
	EmoteID         string   `json:"emote_id"`
	Name            string   `json:"name"`
	ExpectedClasses []string `json:"expected_classes"`
	Note            string   `json:"note"`
	Origin          string   `json:"origin"`
	Advisory        bool     `json:"advisory"`
}

type corpusFile struct {
	Note  string       `json:"note"`
	Cases []corpusCase `json:"cases"`
}

// corpusInflight caps concurrent vision calls. The model shares its GPU with
// live roleplay and with the classification queue, so this stays small;
// EMOTE_CORPUS_PARALLEL overrides it.
var corpusInflight chan struct{}

var corpusCfg *Config

func TestMain(m *testing.M) {
	var cfgPath string
	flag.StringVar(&cfgPath, "cfg-path", "../../cfg/cfg.yaml", "path to config file")
	flag.Parse()

	if raw, err := os.ReadFile(cfgPath); err == nil {
		var file struct {
			EmoteService Config `yaml:"emote_service"`
		}
		if err := yaml.Unmarshal(raw, &file); err == nil {
			file.EmoteService.withDefaults()
			corpusCfg = &file.EmoteService
		}
	}

	parallel := 2
	if v, err := strconv.Atoi(os.Getenv("EMOTE_CORPUS_PARALLEL")); err == nil && v > 0 {
		parallel = v
	}
	corpusInflight = make(chan struct{}, parallel)

	os.Exit(m.Run())
}

type corpusResult struct {
	index    int
	name     string
	expected []string
	got      []string
	advisory bool
	err      error
}

func loadCorpus(t *testing.T) corpusFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "corpus.json"))
	require.NoError(t, err)

	var file corpusFile
	require.NoError(t, json.Unmarshal(raw, &file))
	require.NotEmpty(t, file.Cases)
	return file
}

func TestEmoteCorpus(t *testing.T) {
	if corpusCfg == nil {
		t.Skip("cfg.yaml not readable; pass -cfg-path")
	}
	file := loadCorpus(t)

	ctx := context.Background()
	objects, err := s3client.New(ctx, &corpusCfg.S3)
	require.NoError(t, err)

	// The classifier logs which liquid the second pass named, and that is the
	// first thing to look at when a fluids case moves, so the harness lets it out.
	classifier := NewClassifier(slog.New(slog.NewTextHandler(os.Stderr, nil)),
		oai.New(&corpusCfg.Vision),
		corpusCfg)

	var (
		mu      sync.Mutex
		results []corpusResult
	)
	t.Cleanup(func() { reportCorpus(results) })

	for i, c := range file.Cases {
		t.Run(fmt.Sprintf("%s_%s", c.Name, c.EmoteID[:6]), func(t *testing.T) {
			t.Parallel()

			corpusInflight <- struct{}{}
			defer func() { <-corpusInflight }()

			got, err := classifyCorpusCase(ctx, objects, classifier, c)

			mu.Lock()
			results = append(results, corpusResult{
				index: i, name: c.Name, expected: c.ExpectedClasses, got: got, advisory: c.Advisory, err: err,
			})
			mu.Unlock()

			if err != nil {
				t.Fatalf("%s (%s): %v", c.Name, c.EmoteID, err)
			}
			if slices.Equal(got, FilterContentClasses(c.ExpectedClasses)) {
				return
			}
			if c.Advisory {
				t.Logf("advisory: %s expected %v, got %v — %s", c.Name, c.ExpectedClasses, got, c.Note)
				return
			}
			t.Errorf("%s (%s): expected %v, got %v\n  why this case exists: %s (%s)",
				c.Name, c.EmoteID, c.ExpectedClasses, got, c.Note, c.Origin)
		})
	}
}

func classifyCorpusCase(ctx context.Context, objects *s3client.Client, classifier *Classifier, c corpusCase) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	object, err := objects.GetObject(ctx, s3client.EmotesBucket, "original/"+c.EmoteID+".webp")
	if err != nil {
		return nil, fmt.Errorf("fetch original: %w", err)
	}
	webp, err := io.ReadAll(object)
	object.Close()
	if err != nil {
		return nil, fmt.Errorf("read original: %w", err)
	}

	var grid *Grid
	if len(frameDurations(webp)) > 1 {
		grid, err = classifier.BuildGrid(ctx, webp)
	} else {
		grid, err = classifier.BuildStatic(ctx, webp)
	}
	if err != nil {
		return nil, fmt.Errorf("build grid: %w", err)
	}

	classification, err := classifier.Classify(ctx, c.Name, grid)
	if err != nil {
		return nil, err
	}
	return classification.Classes, nil
}

// reportCorpus prints the per-case table, per-class precision and recall, and
// one CORPUSSTATS line for comparing runs — a prompt change, a model change, or
// the same prompt twice.
func reportCorpus(results []corpusResult) {
	if len(results) == 0 {
		return
	}
	sort.Slice(results, func(i, j int) bool { return results[i].index < results[j].index })

	type counts struct{ tp, fp, fn int }
	perClass := map[string]*counts{}
	bump := func(class string, f func(*counts)) {
		if perClass[class] == nil {
			perClass[class] = &counts{}
		}
		f(perClass[class])
	}

	var pass, fail, advisory, failed int
	fmt.Printf("\n%-22s %-26s %-26s %s\n", "emote", "expected", "got", "verdict")
	for _, r := range results {
		if r.err != nil {
			failed++
			fmt.Printf("%-22s %-26s %-26s ERROR %v\n", r.name, fmt.Sprint(r.expected), "-", r.err)
			continue
		}

		expected := FilterContentClasses(r.expected)
		for _, class := range expected {
			if slices.Contains(r.got, class) {
				bump(class, func(c *counts) { c.tp++ })
			} else {
				bump(class, func(c *counts) { c.fn++ })
			}
		}
		for _, class := range r.got {
			if !slices.Contains(expected, class) {
				bump(class, func(c *counts) { c.fp++ })
			}
		}

		verdict := "ok"
		switch {
		case slices.Equal(r.got, expected):
			pass++
		case r.advisory:
			verdict = "advisory"
			advisory++
		default:
			verdict = "DIFF"
			fail++
		}
		fmt.Printf("%-22s %-26s %-26s %s\n", r.name, fmt.Sprint(expected), fmt.Sprint(r.got), verdict)
	}

	fmt.Printf("\n%-10s %6s %6s %6s %10s %8s\n", "class", "tp", "fp", "fn", "precision", "recall")
	classes := make([]string, 0, len(perClass))
	for class := range perClass {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	for _, class := range classes {
		c := perClass[class]
		fmt.Printf("%-10s %6d %6d %6d %10s %8s\n", class, c.tp, c.fp, c.fn,
			ratio(c.tp, c.tp+c.fp), ratio(c.tp, c.tp+c.fn))
	}

	fmt.Printf("\nCORPUSSTATS cases=%d exact=%d diff=%d advisory=%d errors=%d model=%s version=%d\n",
		len(results), pass, fail, advisory, failed, corpusCfg.Vision.Model, ClassifierVersion)
}

func ratio(num, den int) string {
	if den == 0 {
		return "-"
	}
	return strconv.FormatFloat(float64(num)/float64(den), 'f', 3, 64)
}
