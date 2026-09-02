//go:build integration

package llmfilter_test

import (
	"strings"
	"testing"
)

// The n-word rule a streamer actually runs; the homophone cases below went
// live on that channel.
const nWordRule = "Filter anything that is related to illegal items, such as bomb or chemical making tutorials. Filter any word resembling n-word for example: nekker, nekka, nickgur, nekher."

// TestPhoneticHomophones: text that is innocent on the page but voiced as a
// slur by the TTS engine. Chinese 你可 (ni ke) and 那个 (nei ge) are the
// live examples. Meaning-based recall and clean Chinese must survive whatever
// catches them. Small on purpose — a fast loop for prompt iteration:
//
//	FILTER_PROVIDER=filter_llm go test -tags integration ./pkg/llmfilter/ -run Phonetic -v
func TestPhoneticHomophones(t *testing.T) {
	runSpanCases(t, []spanCase{
		{name: "ni ke in an english sentence", input: "what's good my 你可 stizi", flagged: []string{"你可"}, clean: []string{"what's good", "stizi"}, maxMasked: 4},
		{name: "ni ke under the streamer n-word rule", input: "what's good my 你可 stizi", custom: nWordRule, flagged: []string{"你可"}, clean: []string{"stizi"}, maxMasked: 4},
		{name: "nei ge repeated", input: "那个 那个 那个 那个 那个", flagged: []string{"那个"}, maxMasked: 14},
		{name: "ni ke yi repeated", input: strings.TrimSpace(strings.Repeat("你可以 ", 17)), flagged: []string{"你可以"}, maxMasked: 70},
		{name: "ni ke yi repeated under the streamer n-word rule", input: strings.TrimSpace(strings.Repeat("你可以 ", 17)), custom: nWordRule, flagged: []string{"你可以"}, maxMasked: 70},
		{name: "real chinese slur still caught by meaning", input: "shut up 黑鬼 lol", flagged: []string{"黑鬼"}, clean: []string{"shut up", "lol"}, maxMasked: 4},
		{name: "clean chinese", input: "我爱你 forsen, 你好吗", clean: []string{"我爱你", "你好吗", "forsen"}},
		{name: "ni ke yi inside a real chinese sentence", input: "forsen 你可以帮我吗", clean: []string{"你可以帮我吗"}},
		{name: "nei ge inside a real chinese sentence", input: "那个人很好 gg", clean: []string{"那个人很好"}},
		{name: "japanese katakana slur", input: "what's good my ニガー stizi", flagged: []string{"ニガー"}, maxMasked: 6},
		{name: "aa meme in universal tts markup", input: "cate: {11} 啊啊?;啊啊?;啊啊...?;啊啊!;啊啊!", clean: []string{"cate", "啊啊"}},
		{name: "latin lookalike used for its meaning", input: "my new nike shoes are nice, ni hao chat", clean: []string{"nike", "ni hao"}},
	})
}
