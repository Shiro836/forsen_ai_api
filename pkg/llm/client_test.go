package llm_test

import (
	"app/pkg/llm"
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLLM(t *testing.T) {
	assert := require.New(t)

	client := llm.New(http.DefaultClient, &llm.Config{
		URL:         "http://localhost:8001/v1/completions",
		AccessToken: "forsen_xdd",
		Model:       "solidrust/Llama-3-8B-Lexi-Uncensored-AWQ",
		MaxTokens:   4000,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	response, err := client.Ask(ctx, "<START>###Request: Who is forsen? ###Response: racist. <END>. <START>###Request: What is the power of 2x2? ###Response: ")
	assert.NoError(err)

	assert.NotEmpty(response)

	fmt.Println(response)
}
