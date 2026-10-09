package bidgen

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

const maxDocumentBytes = 2 * 1024 * 1024

var (
	identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	fenceOpen  = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")
	fenceClose = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})[ \\t]*$")
	doneMarker = regexp.MustCompile(`(?m)^ {0,3}<!-- WEKNORA_BID_SECTION_DONE:[a-zA-Z0-9_-]{1,64} -->[ \t]*\r?$`)
)

type protocolBlock struct {
	kind               string
	start, end, bodyAt int
	body               string
	closed             bool
}

// Only top-level fences count. Protocol text inside quoted/indented Markdown or
// an outer code example cannot confirm an outline or complete a section.
func blocks(content string) []protocolBlock {
	var result []protocolBlock
	var current *protocolBlock
	var fence string
	offset := 0
	for _, line := range strings.SplitAfter(content, "\n") {
		plain := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if current == nil {
			if opening := fenceOpen.FindStringSubmatch(plain); opening != nil {
				fence = opening[1]
				current = &protocolBlock{kind: strings.TrimSpace(opening[2]), start: offset, bodyAt: offset + len(line)}
			}
		} else if closing := fenceClose.FindStringSubmatch(plain); closing != nil && closing[1][0] == fence[0] && len(closing[1]) >= len(fence) {
			current.end, current.body, current.closed = offset+len(line), content[current.bodyAt:offset], true
			result = append(result, *current)
			current = nil
		}
		offset += len(line)
	}
	if current != nil {
		current.end, current.body = len(content), content[current.bodyAt:]
		result = append(result, *current)
	}
	return result
}

func decodeStrict(body string, result interface{}) error {
	// encoding/json otherwise silently keeps the last repeated object key.
	// Reject ambiguous model control messages before decoding their fields.
	if uniqueJSONKeys(json.NewDecoder(strings.NewReader(body)), 0) != nil {
		return ErrInvalidInput
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return ErrInvalidInput
	}
	var trailing interface{}
	if decoder.Decode(&trailing) != io.EOF {
		return ErrInvalidInput
	}
	return nil
}

func uniqueJSONKeys(decoder *json.Decoder, depth int) error {
	if depth > 16 {
		return ErrInvalidInput
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	if delim == '{' {
		keys := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || keys[name] {
				return ErrInvalidInput
			}
			keys[name] = true
			if uniqueJSONKeys(decoder, depth+1) != nil {
				return ErrInvalidInput
			}
		}
	} else if delim == '[' {
		for decoder.More() {
			if uniqueJSONKeys(decoder, depth+1) != nil {
				return ErrInvalidInput
			}
		}
	} else {
		return ErrInvalidInput
	}
	_, err = decoder.Token()
	return err
}

func ParsePlan(content string) (*Plan, error) {
	if len(content) > 128*1024 || types.HasUserInputRequest(content) {
		return nil, ErrInvalidInput
	}
	var plan *Plan
	for _, block := range blocks(content) {
		if block.kind != "weknora-bid-plan" {
			continue
		}
		if !block.closed || plan != nil {
			return nil, ErrInvalidInput
		}
		plan = &Plan{}
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(block.body), &fields) != nil || len(fields) != 5 {
			return nil, ErrInvalidInput
		}
		for _, field := range []string{"title", "sections", "facts", "format_notes", "source_notes"} {
			if fields[field] == nil {
				return nil, ErrInvalidInput
			}
		}
		for _, field := range []string{"title", "facts", "format_notes", "source_notes"} {
			var value interface{}
			if json.Unmarshal(fields[field], &value) != nil {
				return nil, ErrInvalidInput
			}
			if _, ok := value.(string); !ok {
				return nil, ErrInvalidInput
			}
		}
		if decodeStrict(block.body, plan) != nil || validatePlan(plan) != nil {
			return nil, ErrInvalidInput
		}
	}
	if plan == nil {
		return nil, ErrInvalidInput
	}
	return plan, nil
}

func validatePlan(plan *Plan) error {
	if !bounded(plan.Title, 200) || strings.ContainsAny(plan.Title, "\r\n") || len(plan.Sections) == 0 || len(plan.Sections) > 40 ||
		utf8.RuneCountInString(plan.Facts) > 12000 || utf8.RuneCountInString(plan.FormatNotes) > 12000 || utf8.RuneCountInString(plan.SourceNotes) > 12000 {
		return ErrInvalidInput
	}
	seen, total := map[string]bool{}, 0
	for i := range plan.Sections {
		section := &plan.Sections[i]
		if !identifier.MatchString(section.ID) || seen[section.ID] || !bounded(section.Title, 200) || strings.ContainsAny(section.Title, "\r\n") ||
			section.TargetWords < 100 || section.TargetWords > 6000 || len(section.Requirements) == 0 || len(section.Requirements) > 20 {
			return ErrInvalidInput
		}
		seen[section.ID] = true
		for j, requirement := range section.Requirements {
			if !bounded(requirement, 500) {
				return ErrInvalidInput
			}
			section.Requirements[j] = strings.TrimSpace(requirement)
		}
		section.Title = strings.TrimSpace(section.Title)
		total += section.TargetWords
	}
	if total > 150000 {
		return ErrInvalidInput
	}
	plan.Title = strings.TrimSpace(plan.Title)
	return nil
}

func bounded(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= limit
}

// CleanContent is for the chat adapter: unwrap explicit draft bodies and remove
// bid-control protocol while preserving weknora-input cards for the existing UI.
func CleanContent(content string) string {
	var out strings.Builder
	offset := 0
	for _, block := range blocks(content) {
		if !strings.HasPrefix(block.kind, "weknora-bid-") {
			continue
		}
		out.WriteString(content[offset:block.start])
		if block.kind == "weknora-bid-body" {
			out.WriteString(block.body)
		}
		offset = block.end
	}
	out.WriteString(content[offset:])
	return strings.TrimSpace(doneMarker.ReplaceAllString(out.String(), ""))
}

func draftContent(content string) string {
	if !types.HasUserInputRequest(content) {
		return CleanContent(content)
	}
	// A question's introductory prose is not bid text. A model can explicitly
	// preserve same-response draft progress before asking for missing facts.
	var drafts []string
	for _, block := range blocks(content) {
		if block.kind == "weknora-bid-body" {
			drafts = append(drafts, block.body)
		}
	}
	return strings.TrimSpace(strings.Join(drafts, "\n\n"))
}

func SectionDoneMarker(sectionID string) string {
	return "<!-- WEKNORA_BID_SECTION_DONE:" + sectionID + " -->"
}

func sectionComplete(output Output, section Section) bool {
	if output.Truncated || types.HasUserInputRequest(output.Content) || utf8.RuneCountInString(strings.TrimSpace(section.Content)) < 40 ||
		!strings.HasSuffix(strings.TrimSpace(output.Content), SectionDoneMarker(section.ID)) ||
		len(doneMarker.FindAllString(output.Content, -1)) != 1 {
		return false
	}
	var coverage *struct {
		SectionID string `json:"section_id"`
		Covered   []int  `json:"covered_requirements"`
	}
	for _, block := range blocks(output.Content) {
		if block.kind != "weknora-bid-coverage" {
			continue
		}
		if !block.closed || coverage != nil {
			return false
		}
		coverage = &struct {
			SectionID string `json:"section_id"`
			Covered   []int  `json:"covered_requirements"`
		}{}
		if decodeStrict(block.body, coverage) != nil || coverage.SectionID != section.ID {
			return false
		}
	}
	if coverage == nil || len(coverage.Covered) != len(section.Requirements) {
		return false
	}
	seen := map[int]bool{}
	for _, index := range coverage.Covered {
		if index < 0 || index >= len(section.Requirements) || seen[index] {
			return false
		}
		seen[index] = true
	}
	return true
}

// MergeContinuation removes an exact repeated prefix/suffix, preserving saved
// facts verbatim. It does not ask another model to rewrite previous chapters.
func MergeContinuation(previous, next string) string {
	next = strings.TrimSpace(next)
	if next == "" || strings.Contains(previous, next) {
		return previous
	}
	if previous == "" {
		return next
	}
	if strings.HasPrefix(next, previous) {
		return previous + next[len(previous):]
	}
	prior, incoming := []rune(previous), []rune(next)
	limit := min(len(prior), len(incoming), 4096)
	for overlap := limit; overlap >= 8; overlap-- {
		if string(prior[len(prior)-overlap:]) == string(incoming[:overlap]) {
			return previous + string(incoming[overlap:])
		}
	}
	return previous + "\n\n" + next
}

// DocumentMarkdown compiles only completed saved sections in their confirmed
// order. Exporters reuse the existing Word formatting renderer on this content.
func DocumentMarkdown(task *Task) (string, error) {
	if task == nil || task.State.Plan == nil || len(task.State.Sections) == 0 || len(task.State.Sections) != len(task.State.Plan.Sections) || task.State.PendingInput != "" {
		return "", ErrInvalidInput
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# %s\n", task.State.Plan.Title)
	for i, section := range task.State.Sections {
		if !section.Completed || section.Truncated || strings.TrimSpace(section.Content) == "" || types.HasUserInputRequest(section.Content) {
			return "", ErrInvalidInput
		}
		if section.ID != task.State.Plan.Sections[i].ID || section.Title != task.State.Plan.Sections[i].Title {
			return "", ErrInvalidInput
		}
		fmt.Fprintf(&out, "\n## %s\n\n%s\n", section.Title, strings.TrimSpace(section.Content))
		if out.Len() > maxDocumentBytes {
			return "", ErrInvalidInput
		}
	}
	return out.String(), nil
}

// ScopeMixError reports that the compiled draft mixes commercial identity or
// pricing chapters into a technical anonymous volume (or the outline mixes
// both volumes). The worker responds by re-planning the outline or resetting
// the named sections, so a separated-volume tender never dead-ends at export.
type ScopeMixError struct {
	// Sections lists the ids whose drafted content leaked commercial scope
	// and must be regenerated under the anonymous-volume drafting rules.
	Sections []string
	// Replan marks outline-level mixing: the plan itself spans both volumes
	// and must be discarded for a single-volume re-plan.
	Replan bool
}

func (e *ScopeMixError) Error() string {
	if e.Replan {
		return "商务标与技术暗标需要分别生成，目录混有两册章节，已退回重新规划单册范围。"
	}
	return "商务标与技术暗标需要分别生成，已重置混入商务身份或报价表述的章节。"
}
