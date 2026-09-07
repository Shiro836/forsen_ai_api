package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestArchiveBodyReplacesImageBytes(t *testing.T) {
	img := Attachment{ID: "img-1", Data: []byte("PNGDATA"), ContentType: "image/png"}
	req := &ChatRequest{
		Model: "m",
		Messages: []Message{
			{Role: "system", StrContent: "sys"},
			{Role: "user", Content: append([]MessageContent{{Type: "text", Text: "look"}}, imageParts([]Attachment{img})...)},
		},
	}

	body := archiveBody(req)
	if strings.Contains(string(body), "base64") {
		t.Fatalf("archived body still carries image bytes: %s", body)
	}
	if !strings.Contains(string(body), `"url":"image:img-1"`) {
		t.Fatalf("archived body lacks the image reference: %s", body)
	}

	// the request itself is untouched: the model still receives the data URI
	sent, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sent), "data:image/png;base64,") {
		t.Fatalf("original request lost its image: %s", sent)
	}
}
