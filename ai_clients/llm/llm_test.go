package llm_test

// import (
// 	"app/ai"
// 	"context"
// 	"fmt"
// 	"net/http"
// 	"testing"

// 	"github.com/stretchr/testify/assert"
// )

// func TestTest(t *testing.T) {
// 	assert := assert.New(t)

// 	aiUrl := "http://localhost:8000/generate"

// 	client := ai.New(http.DefaultClient, &ai.Config{
// 		URL: aiUrl,
// 	})

// 	resp, err := client.Ask(context.Background(), 64, "Context: Forsen is a swedish streamer who streams on twitch and likes lolis. ###User: Who are you? Describe yourself in 10 sentences ###Forsen: I am a streamer on twitch.tv. I usually ")
// 	assert.NoError(err)

// 	fmt.Println(resp)
// }
