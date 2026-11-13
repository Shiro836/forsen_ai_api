package processor

import "testing"

func TestSelectUniversalAICharacter(t *testing.T) {
	known := map[string]bool{"forsen": true, "obiwan": true}
	checkVoice := func(name string) bool { return known[name] }

	tests := []struct {
		name         string
		message      string
		wantName     string
		wantQuestion string
	}{
		{
			name:         "leading selector is removed",
			message:      "forsen: tell me a joke",
			wantName:     "forsen",
			wantQuestion: "tell me a joke",
		},
		{
			name:         "selector mid-message",
			message:      "hey forsen: tell me a joke",
			wantName:     "forsen",
			wantQuestion: "hey tell me a joke",
		},
		{
			name:         "unknown name stays in the question",
			message:      "hitler: tell me a joke",
			wantQuestion: "hitler: tell me a joke",
		},
		{
			name:         "no selector",
			message:      "tell me a joke",
			wantQuestion: "tell me a joke",
		},
		{
			name:         "sound and filter tags stay literal",
			message:      "[166] forsen: {sing} tell me a joke",
			wantName:     "forsen",
			wantQuestion: "[166] {sing} tell me a joke",
		},
		{
			name:         "only the first selector counts",
			message:      "forsen: ask obiwan: tell me a joke",
			wantName:     "forsen",
			wantQuestion: "ask obiwan: tell me a joke",
		},
		{
			name:         "multibyte question",
			message:      "forsen: 世界",
			wantName:     "forsen",
			wantQuestion: "世界",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, question := selectUniversalAICharacter(tt.message, checkVoice)
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if question != tt.wantQuestion {
				t.Errorf("question = %q, want %q", question, tt.wantQuestion)
			}
		})
	}
}
