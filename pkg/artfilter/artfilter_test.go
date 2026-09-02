package artfilter

import (
	"strings"
	"testing"

	"app/pkg/textfilter"
)

func mask(t *testing.T, text string) string {
	t.Helper()
	return Detect(text).Mask(text, "(art)")
}

func TestBrailleWall(t *testing.T) {
	wall := strings.Repeat("⣿⠿⣷⡄", 20)
	got := mask(t, "user asked me: "+wall+" lol")
	want := "user asked me: (art) lol"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBrailleRowsWithSpaces(t *testing.T) {
	got := mask(t, "⣿⣿⣿⣿⣿ ⣿⣿⣿⣿⣿ ⣿⣿⣿⣿⣿")
	if got != "(art)" {
		t.Errorf("rows should bridge into one span, got %q", got)
	}
}

func TestBrailleInterleaveEvasion(t *testing.T) {
	got := mask(t, "⣿!⣿!⣿!⣿!⣿!⣿!⣿!⣿!⣿!⣿")
	if got != "(art)" {
		t.Errorf("interleaved wall should collapse whole, got %q", got)
	}
}

func TestFewBrailleCharsAreNotArt(t *testing.T) {
	in := "some copypasta ⠄⠄⠄ residue"
	if got := mask(t, in); got != in {
		t.Errorf("3 braille chars must not trip the filter, got %q", got)
	}
}

func TestChunkInheritsMessageDecision(t *testing.T) {
	d := Detect("⣿a⣿b⣿c⣿d⣿")
	if !d.BrailleArt {
		t.Fatal("message should detect as braille art")
	}
	if got := d.Mask("chunk with ⣿⣿ only", "(art)"); got != "chunk with (art) only" {
		t.Errorf("chunk braille should mask under message decision, got %q", got)
	}
}

func TestDongerSurvives(t *testing.T) {
	in := "ᕙ(⇀‸↼‶)ᕗ get pumped"
	if got := mask(t, in); got != in {
		t.Errorf("donger must survive, got %q", got)
	}
}

func TestCyrillicSurvives(t *testing.T) {
	in := "опять эти читеры в лобби, ну что за дела"
	if got := mask(t, in); got != in {
		t.Errorf("cyrillic must survive, got %q", got)
	}
}

func TestBoxDrawingRun(t *testing.T) {
	got := mask(t, "look ┌─────┐ box")
	if got != "look (art) box" {
		t.Errorf("got %q", got)
	}
}

func TestSingleArrowSurvives(t *testing.T) {
	in := "go → there and ▶ play"
	if got := mask(t, in); got != in {
		t.Errorf("single symbols must survive, got %q", got)
	}
}

func TestEmojiPresentationExempt(t *testing.T) {
	in := "❤️❤️❤️❤️❤️❤️❤️"
	if got := mask(t, in); got != in {
		t.Errorf("VS16 emoji run must survive, got %q", got)
	}
}

func TestSymbolWall(t *testing.T) {
	got := mask(t, "gg ♦♦♦♦♦♦♦♦ ez")
	if got != "gg (art) ez" {
		t.Errorf("got %q", got)
	}
}

func TestSpansOffsets(t *testing.T) {
	text := "ab ⣿⣿⣿⣿⣿ cd"
	spans := Detect(text).Spans(text)
	if len(spans) != 1 || spans[0].Start != 3 || spans[0].End != 8 {
		t.Errorf("spans = %v", spans)
	}
	if got := textfilter.Censor(text, spans, "(art)"); got != "ab (art) cd" {
		t.Errorf("censor = %q", got)
	}
}

func TestBorderedRowsCollapseToOneSpan(t *testing.T) {
	row := "HH⣿⣿⣿⣿⣿⣿⣿⣿⣿⣿HH"
	got := mask(t, "intro "+row+" "+row+" "+row+" outro")
	if got != "intro (art) outro" {
		t.Errorf("bordered rows should collapse whole, got %q", got)
	}
}

func TestSentenceBetweenArtsStaysSeparate(t *testing.T) {
	got := mask(t, "⣿⣿⣿⣿⣿ hello everyone how is the stream going today ⣿⣿⣿⣿⣿")
	want := "(art) hello everyone how is the stream going today (art)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
