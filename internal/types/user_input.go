package types

import (
	"regexp"
	"strings"
)

// ConversationalUserInputPrompt describes the assistant-output contract consumed
// by the chat UI. Cards stay in ordinary message content and require no new tool,
// endpoint, or database record. Only opted-in agents receive this instruction.
const ConversationalUserInputPrompt = `<conversation_user_input>
When essential details or a decision are missing, let the user answer inside the conversation.
First inspect the current user request, prior answers, attachments and available retrieved evidence. Use available retrieval tools when needed; do not ask the user to repeat facts already established. Current tender requirements and verified company, qualification, performance and product records take priority over historical bid examples. Never invent these facts, prices, commitments, sources or user approval.
Ask only for details needed for the next step. Prefer 1-3 questions, with an absolute maximum of 6 per card. Use single choice, multiple choice or free text as appropriate. Choices must be meaningful alternatives; document-specific factual choices must come from verified evidence. Do not preselect an answer. Use labels and placeholders in the user's language.
Cards collect choices and short factual details. If a tender, certificate or other source document is missing, ask the user in the prose before the card to upload it with the existing chat attachment button. Do not ask them to paste an entire document into a card's text field. Wait for the necessary file to be available before drafting content that depends on it.
End the final assistant message with exactly one JSON code block whose language is weknora-input. You may briefly explain why the details are needed before the block. Do not put the block in a quote, another code block or a generated document.
The schema is {"id":"card-key","title":"Short title","questions":[{"id":"field-key","label":"Question","type":"single|multiple|text","required":true,"options":[{"value":"option-key","label":"Option"}],"placeholder":"Optional text hint"}]}. Use actual type values single, multiple or text. Card id and question ids use only ASCII letters, digits, underscores or hyphens, with 1-64 characters; each question id is unique within the card. These are stable UI keys, not knowledge, session or resource identifiers. Each choice question has 1-12 options with unique non-empty values and labels. Text questions omit options. required is a boolean. The questions array contains 1-6 questions. Keep title within 160 characters, question labels within 500, option values within 200, option labels within 300 and optional placeholders within 300. Omit an empty placeholder. Emit valid JSON without comments or trailing commas.
Example:
` + "```weknora-input\n" + `{"id":"bid-scope-1","title":"Confirm the next drafting step","questions":[{"id":"scope","label":"Which part should be drafted first?","type":"single","required":true,"options":[{"value":"technical","label":"Technical proposal"},{"value":"commercial","label":"Commercial proposal"}]},{"id":"notes","label":"Additional requirements","type":"text","required":false,"placeholder":"Add any requirements not yet covered"}]}` + "\n```\n" + `After emitting a card, STOP this turn and wait for the user's response. Do not fill in missing answers yourself, continue dependent drafting, call further tools or export a document in the same turn. Interpret the next user message as their choices or supplementary details, apply those answers, and continue. Ask a further card only if an essential gap remains; ordinary user text is also a valid response.
For long bid documents, keep the process in this conversation: analyze the tender and its formatting rules, propose the outline, confirm only unresolved requirements, and draft bounded chapters or subsections across turns. Do not attempt tens of thousands of words in a single response. Retain confirmed details and chapter progress in the conversation. Before exporting, resolve essential gaps and preserve the tender's required formatting and previously approved content.
</conversation_user_input>`

var (
	userInputFenceOpen  = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")
	userInputFenceClose = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})[ \\t]*$")
)

// HasUserInputRequest recognizes the reserved top-level clarification fence.
// Malformed or unfinished requests still await user input and must not be
// exported. Quoted, indented and nested code examples remain ordinary content.
func HasUserInputRequest(content string) bool {
	var fence string
	for _, line := range strings.Split(content, "\n") {
		plain := strings.TrimSuffix(line, "\r")
		if fence == "" {
			if match := userInputFenceOpen.FindStringSubmatch(plain); match != nil {
				if strings.TrimSpace(match[2]) == "weknora-input" {
					return true
				}
				fence = match[1]
			}
			continue
		}
		if closing := userInputFenceClose.FindStringSubmatch(plain); closing != nil &&
			closing[1][0] == fence[0] && len(closing[1]) >= len(fence) {
			fence = ""
		}
	}
	return false
}
