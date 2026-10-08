package types

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserInputRequestDistinguishesCardsFromCodeExamples(t *testing.T) {
	card := "```weknora-input\n{\"id\":\"next\"}\n```"
	for _, tc := range []struct {
		name, content string
		want          bool
	}{
		{"request", "请补充资料。\n\n" + card, true},
		{"tilde fence", strings.ReplaceAll(card, "```", "~~~"), true},
		{"long fence", strings.ReplaceAll(card, "```", "````"), true},
		{"CRLF", strings.ReplaceAll(card, "\n", "\r\n"), true},
		{"incomplete", "```weknora-input\n{", true},
		{"malformed JSON", "```weknora-input\nnot json\n```", true},
		{"ordinary mention", "Please use weknora-input for clarification", false},
		{"extra language metadata", strings.Replace(card, "weknora-input", "weknora-input-example", 1), false},
		{"quoted", "> " + strings.ReplaceAll(card, "\n", "\n> "), false},
		{"indented", "    " + strings.ReplaceAll(card, "\n", "\n    "), false},
		{"nested example", "````markdown\n" + card + "\n````", false},
		{"example followed by request", "````markdown\n" + card + "\n````\n" + card, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, HasUserInputRequest(tc.content))
		})
	}
}

func TestUserInputPromptExampleIsValidJSON(t *testing.T) {
	payload := strings.Split(strings.Split(ConversationalUserInputPrompt, "```weknora-input\n")[1], "\n```")[0]
	var example map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(payload), &example))
	require.Equal(t, "bid-scope-1", example["id"])
	require.Len(t, example["questions"], 2)
}
