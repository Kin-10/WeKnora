package session

import (
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

const maxMessageDocumentBytes = 2 * 1024 * 1024

var (
	ErrMessageDocumentNotFound     = errors.New("document message not found")
	ErrMessageDocumentNotReady     = errors.New("document message is not a completed answer")
	ErrMessageDocumentInvalidChain = errors.New("invalid document continuation chain")
	ErrMessageDocumentTooLarge     = errors.New("document content exceeds size limit")
)

type messageDocument struct {
	RootMessageID string
	MessageIDs    []string
	Markdown      string
	Title         string
	Truncated     bool
	IsBid         bool
}

// assembleMessageDocument receives history already authorized by the caller,
// in ascending turn order. It follows only the selected answer's continuation
// ancestry; later answers and unrelated earlier tasks never become document
// content. Historical turns without explicit ancestry use the exact prompts
// emitted by the continuation button, or the two original Chinese commands.
func assembleMessageDocument(messages []*types.Message, targetID string, includeContinuations bool) (messageDocument, error) {
	var document messageDocument
	byID := make(map[string]int, len(messages))
	targetIndex := -1
	for i, message := range messages {
		if message == nil || message.ID == "" {
			continue
		}
		if _, exists := byID[message.ID]; exists {
			return document, fmt.Errorf("%w: duplicate message ID", ErrMessageDocumentInvalidChain)
		}
		byID[message.ID] = i
		if message.ID == targetID {
			targetIndex = i
		}
	}
	if targetIndex < 0 || targetID == "" {
		return document, ErrMessageDocumentNotFound
	}
	target := messages[targetIndex]
	if !documentAnswerReady(target) {
		return document, ErrMessageDocumentNotReady
	}
	if target.SessionID == "" {
		return document, fmt.Errorf("%w: missing session", ErrMessageDocumentInvalidChain)
	}

	indices := []int{targetIndex}
	if includeContinuations {
		visited := map[string]bool{target.ID: true}
		for current := targetIndex; ; {
			message := messages[current]
			parentID := message.ExecutionContext.ContinuationOfMessageID
			previous, continuation, boundaryErr := precedingDocumentContinuation(messages, current, target.SessionID)
			if boundaryErr != nil {
				return document, boundaryErr
			}
			if parentID == "" {
				if !continuation {
					break
				}
			} else {
				parent, exists := byID[parentID]
				if !exists || visited[parentID] || parent >= current || parent != previous || !continuation {
					return document, fmt.Errorf("%w: continuation crosses a turn boundary", ErrMessageDocumentInvalidChain)
				}
				previous = parent
			}
			parent := messages[previous]
			if parent.SessionID != target.SessionID || !documentAnswerReady(parent) ||
				(!parent.CreatedAt.IsZero() && !message.CreatedAt.IsZero() && parent.CreatedAt.After(message.CreatedAt)) {
				return document, fmt.Errorf("%w: parent is not an earlier completed answer in this session", ErrMessageDocumentInvalidChain)
			}
			visited[parent.ID] = true
			indices = append(indices, previous)
			current = previous
		}
	}

	// Joining with a paragraph boundary deliberately retains partial sentences,
	// unfinished tables, and repetitions. Export must not rewrite bid facts.
	var body strings.Builder
	for i := len(indices) - 1; i >= 0; i-- {
		message := messages[indices[i]]
		separator := 0
		if body.Len() > 0 {
			separator = 2
		}
		if len(message.Content) > maxMessageDocumentBytes-body.Len()-separator {
			return messageDocument{}, ErrMessageDocumentTooLarge
		}
		if separator != 0 {
			body.WriteString("\n\n")
		}
		body.WriteString(message.Content)
		document.MessageIDs = append(document.MessageIDs, message.ID)
	}
	document.RootMessageID = document.MessageIDs[0]
	document.Markdown = documentMarkdown(body.String())
	if len(document.Markdown) > maxMessageDocumentBytes {
		return messageDocument{}, ErrMessageDocumentTooLarge
	}
	document.Title = documentTitle(documentMarkdown(messages[indices[len(indices)-1]].Content))
	document.IsBid = messageDocumentIsBid(messages, indices[len(indices)-1], document)
	for _, step := range target.AgentSteps {
		if step.Truncated {
			document.Truncated = true
			break
		}
	}
	return document, nil
}

var (
	bidDocumentRequestRE = regexp.MustCompile(`(?i)(生成|撰写|编写|起草|制作|编制|写|write|draft).*(标书|投标文件|投标书|响应文件|\b(?:bid|tender|bidding)\s+(?:document|proposal|submission)\b)`)
	bidDocumentTitleRE   = regexp.MustCompile(`(?i)(标书|投标文件|投标书|响应文件|\b(?:bid|tender|bidding)\s+(?:document|proposal|submission))\s*(?:[（(][^\n]*[）)])?$`)
	bidDocumentHeadingRE = regexp.MustCompile(`(?m)^\s*#{1,6}\s+([^\n]+)$`)
)

// The export style belongs to the selected task, not the conversation as a
// whole. A root bid-writing request survives later continuation-only turns;
// requests explaining or searching bids remain ordinary Word documents even
// if their answers list the same chapter names.
func messageDocumentIsBid(messages []*types.Message, rootIndex int, document messageDocument) bool {
	// The default cover contains bidder identity fields and is a commercial,
	// identified-bid template. Anonymous technical submissions need their own
	// tender-specific rules, so never apply this template to an explicit 暗标.
	if isAnonymousBidDocument(document.Title) || documentHasAnonymousBidHeading(document.Markdown) {
		return false
	}
	if rootIndex > 0 {
		root, request := messages[rootIndex], messages[rootIndex-1]
		if request != nil && request.Role == "user" && !request.DeletedAt.Valid && request.SessionID == root.SessionID {
			if isAnonymousBidDocument(request.Content) || isBidDiscussion(request.Content) {
				return false
			}
			if bidDocumentRequestRE.MatchString(request.Content) {
				return true
			}
		}
	}
	return looksLikeBidDocument(document.Title, document.Markdown)
}

func isAnonymousBidDocument(text string) bool {
	return strings.Contains(text, "暗标") || strings.Contains(strings.ToLower(text), "anonymous bid") ||
		strings.Contains(strings.ToLower(text), "anonymous tender")
}

func documentHasAnonymousBidHeading(markdown string) bool {
	// A planned table-of-contents entry can mention a later technical 暗标
	// although the generated draft currently contains commercial chapters only.
	// Use actual Markdown chapter headings rather than that prose/list mention.
	for _, heading := range bidDocumentHeadingRE.FindAllStringSubmatch(markdown, -1) {
		if isAnonymousBidDocument(heading[1]) {
			return true
		}
	}
	return false
}

func isBidDiscussion(text string) bool {
	lower := strings.ToLower(text)
	// Scope intent words to the leading request. A bid-writing instruction may
	// legitimately ask that later chapters explain "如何" or "是否" complying.
	if boundary := strings.IndexAny(lower, "，,；;。\n"); boundary >= 0 {
		lower = lower[:boundary]
	}
	for _, phrase := range []string{
		"如何", "怎么", "能否", "是否", "是什么", "什么是", "有哪些", "需要哪些",
		"指南", "教程", "概述", "介绍一下", "解释一下", "介绍标书", "介绍投标",
		"解释标书", "解释投标", "解读标书", "解读投标", "分析标书", "分析投标",
		"how to", "what is", "what are", "explain", "guide to", "tutorial",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	for _, phrase := range []string{"检索", "搜索", "查询", "查找", "search for"} {
		position := strings.Index(lower, phrase)
		if position < 0 {
			continue
		}
		// Retrieval can be the evidence for a writing request, rather than the
		// requested deliverable ("根据检索结果写标书" / "写标书并检索资料").
		authoring := bidDocumentRequestRE.FindStringIndex(lower)
		if authoring != nil && authoring[0] < position {
			continue
		}
		sourceContext := false
		for _, prefix := range []string{"根据", "基于", "参考", "结合", "使用", "利用"} {
			if strings.HasPrefix(strings.TrimSpace(lower), prefix) {
				sourceContext = true
				break
			}
		}
		if !sourceContext {
			return true
		}
	}
	return false
}

// A single-answer export may have no preceding request. Require an explicit
// document title or multiple distinct formal response sections, rather than a
// bid keyword in prose. This also recognizes a continued fragment containing
// several bid chapters without inventing or retrieving missing context.
func looksLikeBidDocument(title, markdown string) bool {
	title = strings.Trim(strings.TrimSpace(title), "*_` ")
	if isAnonymousBidDocument(title) || documentHasAnonymousBidHeading(markdown) || isBidDiscussion(title) {
		return false
	}
	if bidDocumentTitleRE.MatchString(title) {
		return true
	}
	sections := [][]string{
		{"投标函", "响应函", "投标承诺书"},
		{"开标一览表", "投标报价", "报价明细", "报价一览表"},
		{"法定代表人身份证明", "法定代表人授权", "授权委托书"},
		{"资格证明", "企业资质", "资格响应"},
		{"商务响应", "商务偏离"},
		{"技术响应", "技术偏离", "技术参数响应"},
		{"售后服务方案", "售后服务承诺", "项目实施方案"},
	}
	matched := make(map[int]bool)
	for _, heading := range bidDocumentHeadingRE.FindAllStringSubmatch(markdown, -1) {
		for i, alternatives := range sections {
			for _, section := range alternatives {
				if strings.Contains(heading[1], section) {
					matched[i] = true
				}
			}
		}
	}
	return len(matched) >= 2
}

func documentAnswerReady(message *types.Message) bool {
	return message != nil && message.Role == "assistant" && message.IsCompleted &&
		!message.DeletedAt.Valid && strings.TrimSpace(message.Content) != "" &&
		!types.HasUserInputRequest(message.Content)
}

// A continuation edge consists of the preceding assistant answer and exactly
// one continuation request. Any other user instruction ends the old document.
// System/tool rows also end the edge: they are not part of a normal chat turn.
func precedingDocumentContinuation(messages []*types.Message, current int, sessionID string) (int, bool, error) {
	userCount := 0
	continuation := true
	for i := current - 1; i >= 0; i-- {
		message := messages[i]
		if message == nil || message.SessionID != sessionID || message.DeletedAt.Valid {
			return -1, false, fmt.Errorf("%w: history crosses a session or deleted message", ErrMessageDocumentInvalidChain)
		}
		switch message.Role {
		case "assistant":
			return i, userCount == 1 && continuation, nil
		case "user":
			userCount++
			continuation = continuation && isDocumentContinuationPrompt(message.Content)
		default:
			return -1, false, nil
		}
	}
	return -1, false, nil
}

func isDocumentContinuationPrompt(prompt string) bool {
	switch strings.TrimSpace(prompt) {
	case "继续", "继续生成",
		"请从上一条回答的中断处继续，保持原来的语言、结构和编号，只补充尚未完成的内容，不要重复已经生成的部分。",
		"Continue from where the previous answer stopped. Keep its language, structure, and numbering. Add only the unfinished content without repeating what was already generated.",
		"前の回答が途切れたところから続けてください。元の言語、構成、番号を維持し、すでに生成した内容を繰り返さず、未完了の部分だけを追加してください。",
		"Продолжите с места, где закончился предыдущий ответ. Сохраните его язык, структуру и нумерацию. Добавьте только незавершённую часть, не повторяя уже созданное.",
		"이전 답변이 중단된 부분부터 계속해 주세요. 원래 언어, 구조, 번호를 유지하고 이미 생성된 내용을 반복하지 말고 미완성 내용만 추가해 주세요.":
		return true
	default:
		return false
	}
}

var (
	documentCitationTagRE = regexp.MustCompile(`(?i)<(kb|web)\b([^>]*?)/?>`)
	documentAttributeRE   = regexp.MustCompile(`([\w-]+)\s*=\s*"([^"]*)"`)
	documentWikiLinkRE    = regexp.MustCompile(`\[\[([^\]\n]+)\]\]`)
	documentInternalRE    = regexp.MustCompile(`(?i)\b(?:resource|sandbox|local|s3|cos|minio)://[^\s<>\]\)"']+`)
)

// documentMarkdown removes chat-only routing identifiers, retaining readable
// source labels. Ordinary URLs remain links. Nothing here fetches resources or
// interprets HTML; the DOCX writer must encode all other markup as text.
func documentMarkdown(content string) string {
	content = documentCitationTagRE.ReplaceAllStringFunc(content, func(tag string) string {
		parts := documentCitationTagRE.FindStringSubmatch(tag)
		attributes := make(map[string]string)
		for _, attribute := range documentAttributeRE.FindAllStringSubmatch(parts[2], -1) {
			attributes[strings.ToLower(attribute[1])] = html.UnescapeString(attribute[2])
		}
		if strings.EqualFold(parts[1], "kb") {
			if name := strings.TrimSpace(attributes["doc"]); name != "" {
				return "（来源：" + name + "）"
			}
			return ""
		}
		url := strings.TrimSpace(attributes["url"])
		if url == "" {
			return ""
		}
		label := strings.TrimSpace(attributes["title"])
		if label == "" {
			label = url
		}
		label = strings.NewReplacer("[", "\\[", "]", "\\]").Replace(label)
		return "[" + label + "](" + url + ")"
	})
	content = documentWikiLinkRE.ReplaceAllStringFunc(content, func(link string) string {
		inner := link[2 : len(link)-2]
		if _, label, found := strings.Cut(inner, "|"); found {
			return strings.TrimSpace(label)
		}
		if _, label, found := strings.Cut(inner, "/"); found {
			return label
		}
		return inner
	})
	content = documentReadableLinks(content)
	// Standalone storage handles also carry internal routing information.
	return documentInternalRE.ReplaceAllString(content, "[内部附件]")
}

func documentReadableLinks(content string) string {
	var body strings.Builder
	cursor := 0
	for cursor < len(content) {
		relative := strings.Index(content[cursor:], "](")
		if relative < 0 {
			break
		}
		closeBracket := cursor + relative
		openBracket := strings.LastIndexByte(content[:closeBracket], '[')
		inner, end, valid := scanLinkDestination(content, closeBracket+1)
		if !valid || openBracket < cursor || strings.Contains(content[openBracket:closeBracket], "\n") {
			body.WriteString(content[cursor : closeBracket+2])
			cursor = closeBracket + 2
			continue
		}
		image := openBracket > cursor && content[openBracket-1] == '!'
		destination, _ := splitDestinationTitle(inner)
		destination = strings.Trim(strings.TrimSpace(destination), "<>")
		internal := isDocumentInternalDestination(destination)
		if !image && !internal {
			body.WriteString(content[cursor : end+1])
			cursor = end + 1
			continue
		}
		label := content[openBracket+1 : closeBracket]
		start := openBracket
		if image {
			start--
		}
		body.WriteString(content[cursor:start])
		if image {
			if label == "" {
				label = "未命名图片"
			}
			body.WriteString("[图片：" + label + "]")
		} else {
			if label == "" {
				label = "内部附件"
			}
			body.WriteString(label)
		}
		cursor = end + 1
	}
	body.WriteString(content[cursor:])
	return body.String()
}

func isDocumentInternalDestination(destination string) bool {
	lower := strings.ToLower(destination)
	for _, prefix := range []string{"resource:", "sandbox:", "local:", "s3:", "cos:", "minio:"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func documentTitle(markdown string) string {
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		line = strings.TrimSpace(strings.TrimRight(line, "#"))
		if line == "" {
			continue
		}
		if utf8.RuneCountInString(line) > 100 {
			line = string([]rune(line)[:100])
		}
		return line
	}
	return "对话文档"
}
