package documentexport

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/bidformat"
)

func formatPointer[T any](value T) *T { return &value }

func anonymousTenderTestSpec() bidformat.Spec {
	return bidformat.Spec{
		Scope: bidformat.ScopeTechnical, Anonymous: formatPointer(true),
		AllText: bidformat.TextStyle{
			FontFamily: formatPointer("宋体"), FontSizeHalfPoints: formatPointer(28), Color: formatPointer("000000"),
			Bold: formatPointer(false), Italic: formatPointer(false), Underline: formatPointer(false),
			LineSpacingTwips: formatPointer(600), LineRule: formatPointer("exact"), Alignment: formatPointer("left"),
			FirstLineChars: formatPointer(200), SpaceBeforeTwips: formatPointer(0), SpaceAfterTwips: formatPointer(0),
		},
		Page: bidformat.PageStyle{
			Paper: formatPointer("A4"), MarginTopTwips: formatPointer(1417), MarginBottomTwips: formatPointer(1134),
			MarginLeftTwips: formatPointer(1134), MarginRightTwips: formatPointer(1134),
			Header: formatPointer(false), Footer: formatPointer(false), PageNumbers: formatPointer(false),
		},
		Structure: bidformat.Structure{Cover: formatPointer(false), TOC: formatPointer(false), BackCover: formatPointer(false), BlankPages: formatPointer(false)},
	}
}

func TestBuildBidDOCXWithSpecAnonymousAllTextRules(t *testing.T) {
	markdown := "# 技术方案\n\n## 第一章 实施方案\n\n**加粗内容**与*斜体内容*、~~删除线内容~~、`行内代码`保留正文。\n\n" +
		"1. 第一项\n2. 第二项\n   - 子项\n\n> 引用正文\n\n" +
		"```text\n代码内容\n第二行\n```\n\n" +
		"| 序号 | 参数名称 | 响应说明 | 数量 | 备注 |\n| --- | ---: | :---: | --- | --- |\n" +
		"| 1 | 测试参数 | **完全响应**及 " + strings.Repeat("超长参数", 12) + " | 2 | *正文保留* |\n\n## 第二章 服务方案\n\n末尾正文。"
	data, err := BuildBidDOCXWithSpec("技术方案", markdown, anonymousTenderTestSpec())
	if err != nil {
		t.Fatal(err)
	}
	parts := wordParts(t, data)
	document, styles, numbering := parts["word/document.xml"], parts["word/styles.xml"], parts["word/numbering.xml"]
	for _, retained := range []string{"技术方案", "加粗内容", "斜体内容", "删除线内容", "行内代码", "第二项", "子项", "代码内容", "完全响应", "正文保留", "末尾正文。"} {
		if !strings.Contains(document.documentText(), retained) {
			t.Errorf("lost supplied content %q", retained)
		}
	}
	for _, name := range []string{"word/header1.xml", "word/footer1.xml", "word/settings.xml"} {
		if parts[name] != nil {
			t.Errorf("anonymous technical package includes prohibited furniture %s", name)
		}
	}
	for _, name := range []string{"headerReference", "footerReference", "pgNumType", "fldChar", "instrText", "shd", "strike"} {
		if len(document.all(name)) != 0 {
			t.Errorf("anonymous body includes prohibited %s", name)
		}
	}
	if len(document.all("sectPr")) != 1 || len(bidStyledParagraphs(document, "Title")) != 0 || len(bidStyledParagraphs(document, "TOCHeading")) != 0 {
		t.Fatal("anonymous technical bid inherited a business cover or contents")
	}
	margin := document.all("pgMar")[0]
	for key, want := range map[string]string{"top": "1417", "bottom": "1134", "left": "1134", "right": "1134"} {
		if margin.attribute(key) != want {
			t.Errorf("page margin %s = %q, want %s", key, margin.attribute(key), want)
		}
	}
	for partName, part := range map[string]*xmlElement{"document": document, "styles": styles, "numbering": numbering} {
		for _, properties := range part.all("rPr") {
			for _, name := range []string{"sz", "szCs"} {
				values := properties.all(name)
				if len(values) != 1 || values[0].attribute("val") != "28" {
					t.Errorf("%s text property %s does not preserve 14pt", partName, name)
				}
			}
			fonts := properties.all("rFonts")
			if len(fonts) != 1 {
				t.Errorf("%s text lacks uniform font", partName)
				continue
			}
			for _, attr := range []string{"ascii", "hAnsi", "eastAsia", "cs"} {
				if fonts[0].attribute(attr) != "宋体" {
					t.Errorf("%s font %s differs from 宋体", partName, attr)
				}
			}
			for _, name := range []string{"b", "bCs", "i", "iCs"} {
				values := properties.all(name)
				if len(values) != 1 || values[0].attribute("val") != "0" {
					t.Errorf("%s retains decorative %s", partName, name)
				}
			}
			if values := properties.all("u"); len(values) != 1 || values[0].attribute("val") != "none" {
				t.Errorf("%s retains underline", partName)
			}
		}
		for _, properties := range part.all("pPr") {
			spacing := properties.all("spacing")
			if len(spacing) != 1 || spacing[0].attribute("line") != "600" || spacing[0].attribute("lineRule") != "exact" || spacing[0].attribute("before") != "0" || spacing[0].attribute("after") != "0" {
				t.Errorf("%s paragraph violates exact 30pt / zero paragraph spacing", partName)
			}
			alignment := properties.all("jc")
			if len(alignment) != 1 || alignment[0].attribute("val") != "left" {
				t.Errorf("%s paragraph is not left aligned", partName)
			}
			indent := properties.all("ind")
			if len(indent) != 1 || indent[0].attribute("firstLineChars") != "200" || indent[0].attribute("firstLine") != "560" || indent[0].attribute("left") != "0" {
				t.Errorf("%s paragraph violates two-character first-line indent", partName)
			}
		}
	}
	for _, property := range styles.all("pageBreakBefore") {
		if property.attribute("val") != "0" {
			t.Fatal("technical chapters still force business chapter page breaks")
		}
	}
	tables := document.all("tbl")
	if len(tables) != 1 {
		t.Fatal("table is not editable native Word content")
	}
	width := 0
	for _, column := range tables[0].all("gridCol") {
		value, _ := strconv.Atoi(column.attribute("w"))
		width += value
		if value < minimumRuleCellWidth(anonymousTenderTestSpec().AllText) {
			t.Error("specified table font has an unreadable narrow column")
		}
	}
	if width != pageWidth-2268 || len(tables[0].all("trHeight")) > 0 {
		t.Fatal("table overflows margins or clips its wrapped text")
	}
	if creator := parts["docProps/core.xml"].all("creator"); len(creator) != 1 || creator[0].Text != "" {
		t.Fatal("anonymous document adds an author")
	}
}

func TestBuildBidDOCXWithSpecRejectsWideTableInsteadOfReducingFont(t *testing.T) {
	markdown := "## 技术参数\n\n|一|二|三|四|五|六|七|八|九|十|\n|---|---|---|---|---|---|---|---|---|---|\n|1|2|3|4|5|6|7|8|9|10|"
	data, err := BuildBidDOCXWithSpec("技术方案", markdown, anonymousTenderTestSpec())
	if !errors.Is(err, ErrUnsupportedTenderFormat) || len(data) != 0 || !strings.Contains(err.Error(), "拆表") {
		t.Fatalf("wide table silently changed specified type size: data=%d err=%v", len(data), err)
	}
	if _, err := BuildBidDOCX("投标文件", markdown); err != nil {
		t.Fatalf("default business layout regressed: %v", err)
	}
}

func TestBuildBidDOCXWithSpecTableFontExceptionDoesNotInheritBodySize(t *testing.T) {
	spec := anonymousTenderTestSpec()
	spec.Body = spec.AllText
	spec.Heading = spec.AllText
	// The actual tender may expressly exempt chart fonts and type size while
	// still requiring black text without bold, italic or underline everywhere.
	spec.AllText = bidformat.TextStyle{Color: formatPointer("000000"), Bold: formatPointer(false), Italic: formatPointer(false), Underline: formatPointer(false)}
	markdown := "## 技术参数\n\n供应商提供服务方案。\n\n|一|二|三|四|五|六|七|八|九|十|\n|---|---|---|---|---|---|---|---|---|---|\n|**1**|*2*|3|4|5|6|7|8|9|10|"
	data, err := BuildBidDOCXWithSpec("技术方案", markdown, spec)
	if err != nil {
		t.Fatal(err)
	}
	parts := wordParts(t, data)
	tables := parts["word/document.xml"].all("tbl")
	if len(tables) != 1 {
		t.Fatal("chart was lost")
	}
	for _, properties := range tables[0].all("rPr") {
		if size := properties.all("sz"); len(size) != 1 || size[0].attribute("val") != "18" {
			t.Fatal("chart font exception incorrectly inherited body 14pt")
		}
		for _, name := range []string{"b", "bCs", "i", "iCs"} {
			if value := properties.all(name); len(value) != 1 || value[0].attribute("val") != "0" {
				t.Fatalf("chart exception wrongly removed decoration ban %s", name)
			}
		}
		if value := properties.all("color"); len(value) != 1 || value[0].attribute("val") != "000000" {
			t.Fatal("chart exception wrongly removed black-text requirement")
		}
	}
	if len(tables[0].all("shd")) != 0 {
		t.Fatal("anonymous chart retains a gray background")
	}
}

func TestBuildBidDOCXWithSpecBusinessTargetedOverrides(t *testing.T) {
	spec := bidformat.Spec{
		Scope:   bidformat.ScopeBusiness,
		Body:    bidformat.TextStyle{FontFamily: formatPointer("仿宋"), FontSizeHalfPoints: formatPointer(28), LineSpacingTwips: formatPointer(600), LineRule: formatPointer("exact")},
		Heading: bidformat.TextStyle{FontFamily: formatPointer("黑体"), FontSizeHalfPoints: formatPointer(36)},
		Table:   bidformat.TextStyle{FontFamily: formatPointer("宋体"), FontSizeHalfPoints: formatPointer(24)},
		Page:    bidformat.PageStyle{MarginLeftTwips: formatPointer(1984)},
	}
	data, err := BuildBidDOCXWithSpec("投标文件", "## 第一章\n\n正文。\n\n|参数|响应|\n|---|---|\n|参数值|完全响应|", spec)
	if err != nil {
		t.Fatal(err)
	}
	parts := wordParts(t, data)
	sections := bidDocumentSections(t, parts["word/document.xml"])
	if len(sections) != 3 || len(parts["word/document.xml"].all("headerReference")) != 1 || parts["word/footer1.xml"] == nil {
		t.Fatal("partial business rule removed cover, contents or numbering")
	}
	checks := map[string]struct{ font, size string }{"BidBody": {"仿宋", "28"}, "BidFirstChapter": {"黑体", "36"}, "BidTable": {"宋体", "24"}}
	for style, want := range checks {
		paragraphs := bidStyledParagraphs(sections[2], style)
		if len(paragraphs) == 0 {
			t.Fatalf("missing %s text", style)
		}
		for _, p := range paragraphs {
			for _, r := range p.all("rPr") {
				if r.all("rFonts")[0].attribute("eastAsia") != want.font || r.all("sz")[0].attribute("val") != want.size {
					t.Errorf("%s uses another target's formatting", style)
				}
			}
		}
	}
	bodySpacing := bidStyledParagraphs(sections[2], "BidBody")[0].all("spacing")[0]
	if bodySpacing.attribute("line") != "600" || bodySpacing.attribute("lineRule") != "exact" {
		t.Fatal("explicit body line spacing was ignored")
	}
	for _, margin := range parts["word/document.xml"].all("pgMar") {
		if margin.attribute("left") != "1984" {
			t.Fatal("section binding margin did not apply uniformly")
		}
	}
	gridWidth := 0
	for _, column := range sections[2].all("gridCol") {
		value, _ := strconv.Atoi(column.attribute("w"))
		gridWidth += value
	}
	if gridWidth != pageWidth-1984-bidOtherMargin {
		t.Fatal("table ignored tender binding margin")
	}
}

func TestBuildBidDOCXWithSpecNoClaimedAnonymizationOrUnsupportedTemplate(t *testing.T) {
	for name, spec := range map[string]bidformat.Spec{
		"A3 template":          {Page: bidformat.PageStyle{Paper: formatPointer("A3")}},
		"back cover":           {Structure: bidformat.Structure{BackCover: formatPointer(true)}},
		"impossible margins":   {Page: bidformat.PageStyle{MarginLeftTwips: formatPointer(9000), MarginRightTwips: formatPointer(9000)}},
		"invalid font":         {AllText: bidformat.TextStyle{FontFamily: formatPointer("宋体\n黑体")}},
		"unplaced page number": {Page: bidformat.PageStyle{Footer: formatPointer(false), PageNumbers: formatPointer(true)}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildBidDOCXWithSpec("投标文件", "正文。", spec); !errors.Is(err, ErrUnsupportedTenderFormat) {
				t.Fatalf("unsupported requirement was silently replaced: %v", err)
			}
		})
	}
	if _, err := BuildBidDOCXWithSpec("投标文件", "# 投标文件\n\n投标人名称：示例公司\n法定代表人：测试姓名\n\n## 技术响应\n\n正文。", anonymousTenderTestSpec()); !errors.Is(err, ErrUnsupportedTenderFormat) {
		t.Fatalf("business identity was silently claimed anonymous: %v", err)
	}
}

func TestBidSpecSpacingPreservesUnspecifiedDefaultAttributes(t *testing.T) {
	spec := bidformat.Spec{Body: bidformat.TextStyle{SpaceBeforeTwips: formatPointer(0)}}
	styles := parsePart(t, bidSpecStylesXML(spec))
	defaults := styles.all("docDefaults")[0].all("pPr")[0].all("spacing")[0]
	if defaults.attribute("before") != "0" || defaults.attribute("after") != "120" || defaults.attribute("line") != "360" || defaults.attribute("lineRule") != "auto" {
		t.Fatal("partial paragraph rule erased unspecified default spacing")
	}
}

func TestBuildBidDOCXWithSpecStandardCharacterFormattingAndNoShading(t *testing.T) {
	spec := bidformat.Spec{Scope: bidformat.ScopeBusiness, AllText: bidformat.TextStyle{
		Shading: formatPointer(false), CharacterSpacingTwips: formatPointer(0), PositionHalfPoints: formatPointer(0),
	}}
	data, err := BuildBidDOCXWithSpec("投标文件", "## 实施方案\n\n**加粗**和`代码`。\n\n1. 条目\n\n|参数|响应|\n|---|---|\n|测试|完全响应|", spec)
	if err != nil {
		t.Fatal(err)
	}
	parts := wordParts(t, data)
	for partName, part := range parts {
		for _, shading := range part.all("shd") {
			if shading.attribute("val") != "nil" || shading.attribute("fill") != "" {
				t.Errorf("%s retains a background fill", partName)
			}
		}
		if partName != "word/document.xml" && partName != "word/styles.xml" && partName != "word/numbering.xml" {
			continue
		}
		for _, run := range part.all("rPr") {
			if spacing := run.all("spacing"); len(spacing) != 1 || spacing[0].attribute("val") != "0" {
				t.Errorf("%s run lacks standard character spacing", partName)
			}
			if position := run.all("position"); len(position) != 1 || position[0].attribute("val") != "0" {
				t.Errorf("%s run lacks standard character position", partName)
			}
			if shading := run.all("shd"); len(shading) != 1 || shading[0].attribute("val") != "nil" {
				t.Errorf("%s run inherits background fill", partName)
			}
		}
	}
}

func TestBuildBidDOCXWithSpecDoesNotInsertBlankLinesAfterTables(t *testing.T) {
	markdown := "## 一、响应\n\n正文第一行。<br>正文第二行。\n\n|参数|响应|\n|---|---|\n|参数甲|完全响应|\n|参数乙||\n\n## 二、服务\n\n服务正文。\n\n|事项|资料|\n|---|---|\n|培训|待补充|"
	noFrontMatter := bidformat.Structure{Cover: formatPointer(false), TOC: formatPointer(false)}
	for name, spec := range map[string]bidformat.Spec{
		"anonymous technical exact spacing": anonymousTenderTestSpec(),
		"explicit zero paragraph spacing":   {Scope: bidformat.ScopeBusiness, Structure: noFrontMatter, Body: bidformat.TextStyle{SpaceBeforeTwips: formatPointer(0), SpaceAfterTwips: formatPointer(0)}},
		"explicit no blank pages":           {Scope: bidformat.ScopeBusiness, Structure: bidformat.Structure{Cover: formatPointer(false), TOC: formatPointer(false), BlankPages: formatPointer(false)}},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := BuildBidDOCXWithSpec("排版示例", markdown, spec)
			requireNoTenderTableSpacer(t, wordParts(t, data), err)
		})
	}
	// Changing tender formatting must not remove the original template's
	// separators from ordinary document or business exports.
	for name, build := range map[string]func(string, string) ([]byte, error){"ordinary": BuildDOCX, "default business": BuildBidDOCX} {
		t.Run(name, func(t *testing.T) {
			data, err := build("排版示例", markdown)
			if err != nil {
				t.Fatal(err)
			}
			document := wordParts(t, data)["word/document.xml"]
			spacers := 0
			for _, node := range document.all("body")[0].Children {
				if node.Name.Local == "p" && len(node.Children) == 0 {
					spacers++
				}
			}
			if spacers != 2 {
				t.Fatalf("default layout table separators changed: got %d, want 2", spacers)
			}
		})
	}
}

func requireNoTenderTableSpacer(t *testing.T, parts map[string]*xmlElement, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	document := parts["word/document.xml"]
	for _, node := range document.all("body")[0].Children {
		if node.Name.Local == "p" && node.documentText() == "" && len(node.all("sectPr")) == 0 && len(node.all("fldChar")) == 0 && len(node.all("instrText")) == 0 {
			t.Fatal("tender renderer inserted an unrequested empty body paragraph after a table")
		}
	}
	if len(document.all("tbl")) != 2 {
		t.Fatal("native tables were removed when suppressing spacers")
	}
	for _, cell := range document.all("tc") {
		if len(cell.all("p")) == 0 {
			t.Fatal("table cell lost its required native paragraph")
		}
	}
	for _, text := range []string{"正文第一行。", "正文第二行。", "参数甲", "参数乙", "二、服务", "服务正文。", "待补充"} {
		if !strings.Contains(document.documentText(), text) {
			t.Fatalf("removing a spacer lost supplied text %q", text)
		}
	}
	if len(document.all("br")) == 0 {
		t.Fatal("supplied HTML line break was changed while removing template spacers")
	}
}
