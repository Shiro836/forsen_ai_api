package processor

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"app/db"
)

func (s *Service) craftPrompt(char *db.Card, requester string, message string) (string, error) {
	data := char.Data

	messageExamples := &strings.Builder{}
	for _, msgExample := range data.MessageExamples {
		messageExamples.WriteString(fmt.Sprintf("<START>###UserName: %s\n###%s: %s<END>\n", msgExample.Request, data.Name, msgExample.Response))
	}

	prompt := &strings.Builder{}
	prompt.WriteString("Start request/response pairs with <START> and end with <END>\n")
	if len(data.Name) != 0 {
		prompt.WriteString(fmt.Sprintf("Name: %s\n", data.Name))
	}
	if len(data.Description) != 0 {
		prompt.WriteString(fmt.Sprintf("Description: %s\n", data.Description))
	}
	if len(data.Personality) != 0 {
		prompt.WriteString(fmt.Sprintf("Personality: %s\n", data.Personality))
	}
	if len(data.MessageExamples) != 0 {
		prompt.WriteString(fmt.Sprintf("Message Examples: %s", messageExamples.String()))
	}
	if len(data.SystemPrompt) != 0 {
		prompt.WriteString(fmt.Sprintf("System Instructions: %s\n", data.SystemPrompt))
	}

	prompt.WriteString(fmt.Sprintf("Prompt: <START>###%s: %s\n###%s: ", requester, message, data.Name))

	return prompt.String(), nil
}

func (s *Service) FilterText(ctx context.Context, userSettings *db.UserSettings, text string) string {
	swears := GlobalSwears // regex patterns

	if len(userSettings.Filters) != 0 {
		swears = slices.Concat(swears, strings.Split(userSettings.Filters, ","))
	}

	for _, exp := range swears {
		exp = strings.TrimSpace(exp)

		r, err := regexp.Compile("(?i)" + exp) // makes them case-insensitive by default
		if err != nil {
			s.logger.Warn(fmt.Sprintf("failed compiling reg expression '%s'", exp), "err", err)
			continue
		}
		text = r.ReplaceAllString(text, "(filtered)")
	}

	return text
}
