package llmfilter

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

// A slur can live only in the gap between two innocent words ("gain eagers"
// voiced as "gai-neagers"). The model cannot see it: its tokenizer splits that
// as "gain", " e", "agers", so the sound is never in its input.

// Asking first whether the sentence reads as ordinary language is what keeps
// "an eager beaver" clean; a bare "judge the sounds" instruction masks it.
const joinPrompt = "SPOKEN FORM — how text-to-speech will voice the TARGET, as continuous sound with word gaps removed. A slur can hide across a word gap. " +
	"Decide in two steps. First: does the TARGET read as normal, well-formed language, every word ordinary and fitting its sentence? If yes, leave it untouched no matter what the sounds resemble. " +
	"Second: if some word is invented, misspelled or does not belong, and the joined sounds voice a slur, tag that one word.\n"

// Matched by sound, not spelling — a slur that is simply written out is already
// covered by the policy prompt and the per-user regex lists.
//
// "tranny" is deliberately absent: it sounds one edit away from every "trans"
// word, so it fires on transphobe, transmission and unrelated foreign speech.
var slurSounds = map[string][]string{
	"en": {"nigger", "nigga", "niggers", "faggot", "spic", "chink", "beaner", "retard", "negro"},
	"ru": {"пидор", "жид", "хач"},
	"es": {"maricón"},
}

// A stem shorter than this matches too much of ordinary speech once the
// distance allowance is applied.
const minStemLen = 4

var (
	stemsOnce sync.Once
	slurStems []string
)

// Empty when espeak is unavailable, which disables the check. Derived once per
// process off a background context, so one cancelled request cannot disable it.
func stems() []string {
	stemsOnce.Do(func() {
		for voice, words := range slurSounds {
			for _, w := range words {
				ipa, err := espeak(context.Background(), w, voice)
				if err != nil {
					slog.Error("llmfilter: espeak unavailable, cross-word slur sounds will not be checked", "err", err)
					slurStems = nil
					return
				}
				if s := normPhonemes(ipa); len(s) >= minStemLen {
					slurStems = append(slurStems, s)
				}
			}
		}
	})
	return slurStems
}

var cyrillic = regexp.MustCompile(`[\p{Cyrillic}]`)

// Cyrillic through the English voice comes back as English letter names, so
// the caller picks the voice by script.
func espeak(ctx context.Context, text, voice string) (string, error) {
	cmd := exec.CommandContext(ctx, "espeak-ng", "-q", "--ipa", "-v", voice)
	cmd.Stdin = strings.NewReader(text)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func voiceFor(text string) string {
	if cyrillic.MatchString(text) {
		return "ru"
	}
	return "en"
}

var (
	phonemeNoise = regexp.MustCompile(`[ˈˌ‿"'ːʲʰ\s.,?!;:()\[\]-]+`)
	digraphs     = strings.NewReplacer("tʃ", "C", "dʒ", "J", "əʊ", "O", "oʊ", "O", "aɪ", "Y", "eɪ", "E", "aʊ", "W")
	phonemeClass = map[rune]rune{
		'i': 'I', 'ɪ': 'I',
		'e': 'A', 'ɛ': 'A', 'æ': 'A', 'a': 'A', 'ɐ': 'A',
		'ə': '@', 'ʌ': '@', 'ɚ': '@', 'ɜ': '@',
		'u': 'U', 'ʊ': 'U', 'ʉ': 'U',
		'ɒ': 'Q', 'ɔ': 'Q', 'ɑ': 'Q',
		'o': 'O', 'ɡ': 'g', 'ɹ': 'r', 'r': 'r', 'ɾ': 'r', 'ʁ': 'r',
		'ʃ': 'S', 'ʂ': 'S', 'ɕ': 'S', 'ʒ': 'Z', 'ʐ': 'Z', 'ʑ': 'Z',
	}
)

// Coarse classes so a near-homophone still matches: the attack voices "niːɡəz"
// where the slur itself is "nɪɡəz".
func normPhonemes(ipa string) string {
	s := digraphs.Replace(phonemeNoise.ReplaceAllString(ipa, ""))
	var b strings.Builder
	for _, r := range s {
		if c, ok := phonemeClass[r]; ok {
			b.WriteRune(c)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// The only wordlist on the box, and English-only, so callers apply it to the
// English voice alone.
const wordListPath = "/usr/share/cracklib/cracklib-small"

var (
	dictOnce  sync.Once
	dictWords map[string]bool
)

// Empty when the list is missing, which leaves ordinary speech to the model.
func dictionary() map[string]bool {
	dictOnce.Do(func() {
		b, err := os.ReadFile(wordListPath)
		if err != nil {
			slog.Warn("llmfilter: no wordlist, ordinary speech that voices a slur may be flagged", "path", wordListPath, "err", err)
			return
		}
		dictWords = make(map[string]bool)
		for _, w := range strings.Split(string(b), "\n") {
			if w = strings.TrimSpace(strings.ToLower(w)); w != "" {
				dictWords[w] = true
			}
		}
	})
	return dictWords
}

// A sentence of nothing but real words that voices a slur is a coincidence of
// pronunciation ("an eager beaver"); an attack has to smuggle in a word that is
// not one.
func allDictionary(text string) bool {
	dict := dictionary()
	if len(dict) == 0 {
		return false
	}
	for _, f := range strings.Fields(strings.ToLower(text)) {
		w, _, _ := strings.Cut(strings.Trim(f, `.,!?;:"'()[]`), "'")
		if w == "" {
			continue
		}
		if !dict[w] {
			return false
		}
	}
	return true
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(min(prev[j]+1, cur[j-1]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func contains(sound, stem string) bool {
	maxDist := 0
	if len([]rune(stem)) >= 5 {
		maxDist = 1
	}
	return containsDist(sound, stem, maxDist)
}

func containsDist(sound, stem string, maxDist int) bool {
	rs, rt := []rune(sound), []rune(stem)
	for size := len(rt) - maxDist; size <= len(rt)+maxDist; size++ {
		if size < minStemLen {
			continue
		}
		for i := 0; i+size <= len(rs); i++ {
			if levenshtein(string(rs[i:i+size]), stem) <= maxDist {
				return true
			}
		}
	}
	return false
}

// joinHint returns the SPOKEN FORM block when the target's joined pronunciation
// carries a slur that no single word carries, and "" otherwise. Scripts
// spokenForm covers are skipped: espeak mangles them.
func joinHint(ctx context.Context, text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	for _, r := range text {
		if hintedScript(r) {
			return ""
		}
	}
	st := stems()
	if len(st) == 0 {
		return ""
	}

	voice := voiceFor(text)
	ipa, err := espeak(ctx, text, voice)
	if err != nil {
		slog.Error("llmfilter: espeak failed, skipping cross-word slur sounds", "err", err)
		return ""
	}
	joined := normPhonemes(ipa)

	hit := ""
	for _, s := range st {
		if contains(joined, s) {
			hit = s
			break
		}
	}
	if hit == "" {
		return ""
	}

	// A stem inside one word is a spelled slur, already handled by the policy
	// prompt and the regex lists.
	for _, w := range strings.Fields(text) {
		wi, err := espeak(ctx, w, voice)
		if err != nil {
			break
		}
		if containsDist(normPhonemes(wi), hit, 0) {
			return ""
		}
	}
	if voice == "en" && allDictionary(text) {
		return ""
	}
	return joinPrompt + strings.TrimSpace(joined) + "\n\n"
}
