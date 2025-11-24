package processor

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"app/db"
	"app/pkg/charutil"
)

func (s *Service) craftPrompt(char *db.Card, requester string, message string, history ...string) (string, error) {
	data := char.Data

	prompt := &strings.Builder{}

	prompt.WriteString(charutil.BuildCharacterContext(data.Name, data.Description, data.Personality, data.MessageExamples))

	if len(data.SystemPrompt) != 0 {
		prompt.WriteString(fmt.Sprintf("System Instructions: %s\n", data.SystemPrompt))
	}

	for _, turn := range history {
		if turn != "" {
			prompt.WriteString(fmt.Sprintf("<START>%s<END>\n", turn))
		}
	}

	prompt.WriteString(fmt.Sprintf("Prompt: <START>###%s: %s\n###%s: ", requester, message, data.Name))

	return prompt.String(), nil
}

func (s *Service) dialoguePrompt(char *db.Card, scenario string, history ...string) (string, error) {
	data := char.Data

	prompt := &strings.Builder{}

	prompt.WriteString(charutil.BuildCharacterContext(data.Name, data.Description, data.Personality, data.MessageExamples))

	if len(data.SystemPrompt) != 0 {
		prompt.WriteString(fmt.Sprintf("System Instructions: %s\n", data.SystemPrompt))
	}

	if scenario != "" {
		prompt.WriteString(fmt.Sprintf("Topic: %s\n", scenario))
	}

	for _, turn := range history {
		if turn != "" {
			prompt.WriteString(fmt.Sprintf("<START>%s<END>\n", turn))
		}
	}

	prompt.WriteString(fmt.Sprintf("<START>%s: ", data.Name))

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
