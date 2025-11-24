package charutil

import (
	"fmt"
	"strings"

	"app/db"
)

// BuildCharacterContext creates a formatted context string for a character
func BuildCharacterContext(name, description, personality string, messageExamples []db.MessageExample) string {
	context := &strings.Builder{}

	if len(name) != 0 {
		context.WriteString(fmt.Sprintf("Name: %s\n", name))
	}
	if len(description) != 0 {
		context.WriteString(fmt.Sprintf("Description: %s\n", description))
	}
	if len(personality) != 0 {
		context.WriteString(fmt.Sprintf("Personality: %s\n", personality))
	}
	if len(messageExamples) != 0 {
		examples := &strings.Builder{}
		for _, msgExample := range messageExamples {
			examples.WriteString(fmt.Sprintf("<START>###UserName: %s\n###%s: %s<END>\n", msgExample.Request, name, msgExample.Response))
		}
		context.WriteString(fmt.Sprintf("Message Examples: %s", examples.String()))
	}

	return context.String()
}
