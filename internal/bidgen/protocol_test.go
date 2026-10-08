package bidgen

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanProtocolRejectsAmbiguousUnsafeOversizedAndNestedPlans(t *testing.T) {
	valid := fixturePlan(1)
	plan, err := ParsePlan(valid)
	require.NoError(t, err)
	require.Len(t, plan.Sections, 1)
	for _, invalid := range []string{
		"````markdown\n" + valid + "\n````",
		valid + "\n" + valid,
		strings.Replace(valid, `"id":"a"`, `"id":"../../other-session"`, 1),
		strings.Replace(valid, `"target_words":200`, `"target_words":6001`, 1),
		strings.Replace(valid, `"target_words":200`, `"target_words":200,"unknown":true`, 1),
		strings.Replace(valid, `"title":"采购项目投标文件"`, `"title":"采购项目投标文件","title":"overwrite"`, 1),
		strings.Replace(valid, `"facts":`, `"unknown":`, 1),
		valid[:len(valid)-1],
		valid + "\n```weknora-input\n{}\n```",
	} {
		_, err := ParsePlan(invalid)
		require.ErrorIs(t, err, ErrInvalidInput, invalid)
	}
	large, _ := ParsePlan(fixturePlan(40))
	require.NotNil(t, large)
	large.Sections = append(large.Sections, large.Sections[0])
	body, _ := json.Marshal(large)
	_, err = ParsePlan("```weknora-bid-plan\n" + string(body) + "\n```")
	require.ErrorIs(t, err, ErrInvalidInput)
}

func TestCleanContentPreservesCardsButSavedDraftExcludesClarificationProse(t *testing.T) {
	card := "```weknora-input\n{\"id\":\"ask\"}\n```"
	content := "```weknora-bid-body\n" + substantialBody + "\n```\n请补充交期。\n" + card
	clean := CleanContent(content)
	require.Contains(t, clean, substantialBody)
	require.Contains(t, clean, card)
	require.NotContains(t, clean, "weknora-bid-body")
	require.Equal(t, substantialBody, draftContent(content))
	require.Empty(t, draftContent("请先确认项目。\n"+card))
	finished := completedSection("a", substantialBody, "done")
	require.Equal(t, substantialBody, CleanContent(finished.Content))
	require.Equal(t, "说明：", CleanContent("说明：\n```weknora-bid-plan\n{\"partial\":"))
}

func TestContinuationOverlapPreservesChineseFactsTablesAndOriginalText(t *testing.T) {
	for _, tc := range []struct {
		previous, next, want string
	}{
		{"供应商：核验公司。金额：1234万元。", "金额：1234万元。交期：30天。", "供应商：核验公司。金额：1234万元。交期：30天。"},
		{"| 参数 | 响应 |\n|---|---|\n| 数量 | 10", "| 数量 | 10套 |", "| 参数 | 响应 |\n|---|---|\n| 数量 | 10套 |"},
		{"正文已经保存。", "正文已经保存。", "正文已经保存。"},
		{"正文已经保存。", "正文已经保存。下一段。", "正文已经保存。下一段。"},
		{"已有章节。", "新小节。", "已有章节。\n\n新小节。"},
	} {
		require.Equal(t, tc.want, MergeContinuation(tc.previous, tc.next))
	}
}

func TestCompileRejectsMissingPendingOrTruncatedSections(t *testing.T) {
	spec := SectionSpec{ID: "a", Title: "章节"}
	task := &Task{State: State{Plan: &Plan{Title: "标书", Sections: []SectionSpec{spec}}, Sections: []Section{{SectionSpec: spec, Completed: true, Content: substantialBody}}}}
	_, err := DocumentMarkdown(task)
	require.NoError(t, err)
	task.State.PendingInput = "missing"
	_, err = DocumentMarkdown(task)
	require.ErrorIs(t, err, ErrInvalidInput)
	task.State.PendingInput = ""
	task.State.Sections[0].Truncated = true
	_, err = DocumentMarkdown(task)
	require.ErrorIs(t, err, ErrInvalidInput)
	task.State.Sections[0].Truncated = false
	task.State.Sections[0].Content = "```weknora-input\n{}\n```"
	_, err = DocumentMarkdown(task)
	require.ErrorIs(t, err, ErrInvalidInput)
}
