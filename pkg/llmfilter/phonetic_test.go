package llmfilter

import (
	"context"
	"os/exec"
	"testing"
)

func requireEspeak(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("espeak-ng"); err != nil {
		t.Skip("espeak-ng not in PATH")
	}
}

func TestJoinHintFiresOnCrossWordSlurs(t *testing.T) {
	requireEspeak(t)
	for _, text := range []string{
		"avenavenaven asked me: would you rather gain muscles or gain eagers?",
		"my main eagers just showed up lol",
		"he plays like a free tard every round",
		"he is a lone gro in this lobby",
		"слышь ты купи дор, иди сюда",
	} {
		if joinHint(context.Background(), text) == "" {
			t.Errorf("expected a spoken-form block for %q", text)
		}
	}
}

func TestJoinHintLeavesOrdinaryChatAlone(t *testing.T) {
	requireEspeak(t)
	for _, text := range []string{
		"hello mister stizi what is this game about",
		"poggers that was a sick play gg",
		"can you sing a song about bajs",
		"would you rather gain muscles or gain weight",
		"do not be a nerd about it, just queue",
		"sorry we're tardy today, the stream starts late",
		"your top 5 favorite 4th/5th gen kpop idols",
		"opinion on transsenn? is he retarded or chill guy?",
		"bazd meg, kurva anyád",
		"stizi is the most based transphobe, those people are just synthetic hormone-fueled confusion",
		"my car's transmission is busted again",
		"",
	} {
		if h := joinHint(context.Background(), text); h != "" {
			t.Errorf("unexpected spoken-form block for %q: %q", text, h)
		}
	}
}

func TestJoinHintSkipsSpelledSlurs(t *testing.T) {
	requireEspeak(t)
	if h := joinHint(context.Background(), "shut up you nigger"); h != "" {
		t.Errorf("spelled slur should not produce a sound block, got %q", h)
	}
}

func TestJoinHintSkipsScriptsSpokenFormCovers(t *testing.T) {
	requireEspeak(t)
	if h := joinHint(context.Background(), "what's good my 你可 stizi"); h != "" {
		t.Errorf("CJK is spokenForm's job, got %q", h)
	}
}

func TestJoinHintLeavesAllRealWordsAlone(t *testing.T) {
	requireEspeak(t)
	if len(dictionary()) == 0 {
		t.Skip("no wordlist at " + wordListPath)
	}
	for _, text := range []string{
		"he's an eager beaver who wants to gain energy before the boss",
		"when one grows older the aim goes first",
		"is this pick good for the boss fight",
		"how much ink does this printer actually use",
	} {
		if h := joinHint(context.Background(), text); h != "" {
			t.Errorf("ordinary words should not produce a sound block for %q: %q", text, h)
		}
	}
}

func TestAllDictionaryHandlesContractionsAndPunctuation(t *testing.T) {
	if len(dictionary()) == 0 {
		t.Skip("no wordlist at " + wordListPath)
	}
	if !allDictionary("he's an eager beaver, who wants energy?") {
		t.Error("contractions and punctuation should still read as real words")
	}
	if allDictionary("would you rather gain muscles or gain eagers") {
		t.Error("an invented word must not read as a real word")
	}
}

func TestNormPhonemesFoldsLengthAndStress(t *testing.T) {
	if got, want := normPhonemes("ɡˈeɪn ˈiːɡəz"), "gEnIg@z"; got != want {
		t.Errorf("normPhonemes = %q, want %q", got, want)
	}
}

func TestContainsAllowsOneEditForLongStems(t *testing.T) {
	if !contains("lOngrO", "nIgrO") {
		t.Error("expected a one-edit match for a dropped vowel")
	}
	if contains("hAlOwUrld", "nIg@z") {
		t.Error("unrelated sounds must not match")
	}
}
