package bidformat

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func fixtureSource(t *testing.T, name string) Source {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + "-format.txt")
	if err != nil {
		t.Fatal(err)
	}
	return Source{ID: "fixture-" + name, Name: name + "招标文件.pdf", Text: string(data)}
}

func wantValue[T comparable](t *testing.T, field string, value *T, want T) {
	t.Helper()
	if value == nil || *value != want {
		t.Fatalf("%s: got %v, want %v", field, value, want)
	}
}

func TestParseHandanExplicitAnonymousRequirements(t *testing.T) {
	source := fixtureSource(t, "handan")
	result := Parse([]Source{source})
	spec, issues := result.Resolve(ScopeTechnical)
	for _, issue := range issues {
		if issue.Code == "conflict" {
			t.Fatalf("duplicate tender requirements conflict: %+v", issue)
		}
	}
	wantValue(t, "body font", spec.Body.FontFamily, "宋体")
	wantValue(t, "heading font", spec.Heading.FontFamily, "宋体")
	wantValue(t, "body size", spec.Body.FontSizeHalfPoints, 28)
	wantValue(t, "heading size", spec.Heading.FontSizeHalfPoints, 28)
	wantValue(t, "black", spec.AllText.Color, "000000")
	wantValue(t, "bold", spec.AllText.Bold, false)
	wantValue(t, "italic", spec.AllText.Italic, false)
	wantValue(t, "underline", spec.AllText.Underline, false)
	wantValue(t, "line", spec.Body.LineSpacingTwips, 600)
	wantValue(t, "line rule", spec.Body.LineRule, "exact")
	wantValue(t, "alignment", spec.Body.Alignment, "left")
	wantValue(t, "heading alignment", spec.Heading.Alignment, "left")
	wantValue(t, "first line", spec.Body.FirstLineChars, 200)
	wantValue(t, "paragraph after", spec.Body.SpaceAfterTwips, 0)
	wantValue(t, "top", spec.Page.MarginTopTwips, 1417)
	wantValue(t, "bottom", spec.Page.MarginBottomTwips, 1134)
	wantValue(t, "left", spec.Page.MarginLeftTwips, 1134)
	wantValue(t, "right", spec.Page.MarginRightTwips, 1134)
	wantValue(t, "paper", spec.Page.Paper, "A4")
	wantValue(t, "cover", spec.Structure.Cover, false)
	wantValue(t, "toc", spec.Structure.TOC, false)
	wantValue(t, "header", spec.Page.Header, false)
	wantValue(t, "footer", spec.Page.Footer, false)
	wantValue(t, "page numbers", spec.Page.PageNumbers, false)
	wantValue(t, "anonymous", spec.Anonymous, true)
	if spec.Table.FontFamily != nil || spec.Table.FontSizeHalfPoints != nil {
		t.Fatal("explicitly unrestricted table font must not be invented")
	}
	for _, rule := range result.Rules {
		if rule.Scope != ScopeTechnical {
			t.Fatalf("technical-only requirement leaked: %+v", rule)
		}
		if rule.Source.ID != source.ID || len(rule.Source.SHA256) != 64 || rule.Source.Text != "" || !strings.Contains(source.Text, rule.Quote) {
			t.Fatalf("missing original evidence: %+v", rule)
		}
		if rule.Source.Page != 15 && rule.Source.Page != 16 && rule.Source.Page != 81 {
			t.Fatalf("physical source page lost: %+v", rule)
		}
	}
	business, businessIssues := result.Resolve(ScopeBusiness)
	if business.Body.FontFamily != nil || business.Structure.Cover != nil || len(business.Rules) != 0 || len(businessIssues) != 1 || businessIssues[0].Code != "missing" {
		t.Fatalf("anonymous font cannot apply to business volume: %+v, %+v", business, businessIssues)
	}
}

func TestFullSourceDoesNotSpreadAnAnonymousMention(t *testing.T) {
	text := "技术标（暗标）不得出现供应商名称。\n" + strings.Repeat("招标文件其他条款不涉及排版。\n", 400) + "\n商务标制作要求：标题字体为黑体，三号。\n技术标（暗标）制作要求：正文字体为宋体，四号。"
	r := Parse([]Source{{ID: "full", Name: "招标文件", Text: text}})
	business, issues := r.Resolve(ScopeBusiness)
	wantValue(t, "business font", business.Heading.FontFamily, "黑体")
	if business.Body.FontFamily != nil || business.Anonymous != nil || len(issues) != 0 {
		t.Fatalf("a prior anonymous mention must not apply technical styles to whole source: %+v %+v", business, issues)
	}
	technical, _ := r.Resolve(ScopeTechnical)
	wantValue(t, "technical body", technical.Body.FontFamily, "宋体")
}

func TestPreserveUnclearMainContentsPageNumberScope(t *testing.T) {
	r := Parse([]Source{{ID: "main", Name: "招标文件", Text: "主目录\n注：供应商需自行编制页码。\n技术标（暗标）制作要求：不得设置页码。" + strings.Repeat("其他条款。", 900)}})
	technical, issues := r.Resolve(ScopeTechnical)
	wantValue(t, "technical no page number", technical.Page.PageNumbers, false)
	if len(issues) != 1 || issues[0].Property != "page.page_numbers_scope" || issues[0].Code != "unsupported" {
		t.Fatalf("unknown main volume scope silently applied: %+v", issues)
	}
}

func TestParseBaodingDoesNotBorrowFixedLineOrNoCover(t *testing.T) {
	result := Parse([]Source{fixtureSource(t, "baoding")})
	spec, issues := result.Resolve(ScopeTechnical)
	wantValue(t, "body font", spec.Body.FontFamily, "宋体")
	wantValue(t, "heading font", spec.Heading.FontFamily, "宋体")
	wantValue(t, "size", spec.Body.FontSizeHalfPoints, 28)
	wantValue(t, "line", spec.Body.LineSpacingTwips, 360)
	wantValue(t, "line rule", spec.Body.LineRule, "auto")
	wantValue(t, "first line", spec.Body.FirstLineChars, 200)
	wantValue(t, "top", spec.Page.MarginTopTwips, 1417)
	wantValue(t, "bottom", spec.Page.MarginBottomTwips, 1417)
	wantValue(t, "left", spec.Page.MarginLeftTwips, 1417)
	wantValue(t, "right", spec.Page.MarginRightTwips, 1417)
	wantValue(t, "toc", spec.Structure.TOC, false)
	wantValue(t, "cover", spec.Structure.Cover, true)
	wantValue(t, "back cover", spec.Structure.BackCover, false)
	wantValue(t, "blank pages", spec.Structure.BlankPages, false)
	wantValue(t, "bold", spec.AllText.Bold, false)
	wantValue(t, "underline", spec.AllText.Underline, false)
	wantValue(t, "no background", spec.AllText.Shading, false)
	wantValue(t, "character spacing", spec.AllText.CharacterSpacingTwips, 0)
	wantValue(t, "baseline", spec.AllText.PositionHalfPoints, 0)
	wantValue(t, "page numbers", spec.Page.PageNumbers, false)
	var templateIssue bool
	for _, rule := range result.Rules {
		if rule.Source.Page != 57 {
			t.Fatalf("printed page number overwrote physical page: %+v", rule.Source)
		}
	}
	for _, issue := range issues {
		if issue.Code == "conflict" {
			t.Fatalf("unexpected conflict: %+v", issue)
		}
		if issue.Property == "structure.cover_template" {
			templateIssue = true
		}
	}
	if !templateIssue {
		t.Fatal("specified cover template must remain an issue")
	}
}

func TestUniversalRuleConflictsWithLocalDecoration(t *testing.T) {
	result := Parse([]Source{{ID: "one", Name: "招标文件", Text: "所有字体不得加粗。标题字体为黑体，三号，加粗。"}})
	spec, issues := result.Resolve(ScopeBusiness)
	if spec.AllText.Bold != nil || spec.Heading.Bold != nil {
		t.Fatal("universal/local contradictory decoration must remain unresolved")
	}
	if len(issues) != 1 || issues[0].Code != "conflict" || issues[0].Property != "heading.bold" {
		t.Fatalf("missing universal/local conflict: %+v", issues)
	}
	if strings.Contains(spec.Summary(), "bold=") {
		t.Fatal("resolved prompt must not emit contradictory requirements")
	}
}

func TestParseGenericBusinessAndTechnicalScopes(t *testing.T) {
	result := Parse([]Source{{ID: "scopes", Name: "本项目招标文件", Text: "投标文件格式要求：正文字体为宋体，小四号。\n商务标编制要求：标题字体为黑体，三号。\n技术标（暗标）编制要求：标题、正文字体为宋体，四号。不得设置页眉、页脚、页码。"}})
	business, issues := result.Resolve(ScopeBusiness)
	wantValue(t, "business body", business.Body.FontSizeHalfPoints, 24)
	wantValue(t, "business heading", business.Heading.FontFamily, "黑体")
	if len(issues) != 0 {
		t.Fatalf("unexpected business issues: %+v", issues)
	}
	technical, issues := result.Resolve(ScopeTechnical)
	if technical.Body.FontSizeHalfPoints != nil {
		t.Fatal("contradictory general and technical font sizes must not pick one silently")
	}
	var conflict bool
	for _, issue := range issues {
		if issue.Code == "conflict" && issue.Property == "body.font_size_half_points" {
			conflict = true
		}
	}
	if !conflict {
		t.Fatalf("font conflict lost: %+v", issues)
	}
}

func TestParseSourceConflictUnrecognizedAndMissing(t *testing.T) {
	result := Parse([]Source{{ID: "a", Name: "招标文件", Text: "正文字体为宋体，四号。"}, {ID: "b", Name: "更正文件", Text: "正文字体为黑体，小四号。"}})
	spec, issues := result.Resolve(ScopeBusiness)
	if spec.Body.FontFamily != nil || spec.Body.FontSizeHalfPoints != nil || len(issues) != 2 {
		t.Fatalf("conflicts silently resolved: %+v %+v", spec, issues)
	}
	unknown := Parse([]Source{{ID: "c", Name: "文件", Text: "正文字体为华文中宋，四号；正文行距固定为一指宽；页面边距按附件图示。"}})
	_, issues = unknown.Resolve(ScopeBusiness)
	if len(issues) != 3 {
		t.Fatalf("unknown requirements silently ignored: %+v", issues)
	}
	missing := Parse([]Source{{ID: "empty", Name: "空附件"}, {Text: "字体为宋体。"}})
	if len(missing.Issues) != 2 {
		t.Fatalf("missing evidence not reported: %+v", missing)
	}
}

func TestForbiddenAndOptionalFontNamesAreNotPositiveRules(t *testing.T) {
	for _, text := range []string{"正文字体不得使用宋体四号。", "正文字体可采用宋体四号。"} {
		spec, issues := Parse([]Source{{ID: "font", Name: "招标文件", Text: text}}).Resolve(ScopeBusiness)
		if spec.Body.FontFamily != nil || spec.Body.FontSizeHalfPoints != nil || len(issues) != 1 {
			t.Fatalf("excluded/optional font was invented as a required style: %+v %+v", spec, issues)
		}
	}
}

func TestParseOriginalQuotesAndJSONSnapshot(t *testing.T) {
	text := "技术标（暗标）制作要求：\n标题及正文所用文字均采用“宋体”四号“常规”\n字；正文行间距为固定值 30 磅；页边距上 2.5 厘米，其余均为 2 厘米；不得设\n置目录。"
	result := Parse([]Source{{ID: "doc-id", Name: "附件.pdf", Page: 16, ChunkID: "chunk-id", Text: text}})
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var restored Result
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	spec, issues := restored.Resolve(ScopeTechnical)
	if len(issues) != 0 {
		t.Fatalf("unexpected snapshot issues: %+v", issues)
	}
	wantValue(t, "font", spec.Body.FontFamily, "宋体")
	wantValue(t, "size", spec.Body.FontSizeHalfPoints, 28)
	wantValue(t, "line", spec.Body.LineSpacingTwips, 600)
	wantValue(t, "toc", spec.Structure.TOC, false)
	for _, rule := range restored.Rules {
		if !strings.Contains(text, rule.Quote) || rule.Source.Page != 16 || rule.Source.ChunkID != "chunk-id" {
			t.Fatalf("quote/provenance changed: %+v", rule)
		}
	}
}
