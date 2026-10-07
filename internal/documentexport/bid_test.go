package documentexport

import (
	"bytes"
	"encoding/xml"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// A section's properties are attached to its final paragraph, except for the
// document's final section. Keep the front matter separate from the body when
// checking facts, navigation and page numbering.
func bidDocumentSections(t *testing.T, document *xmlElement) []*xmlElement {
	t.Helper()
	bodies := document.all("body")
	if len(bodies) != 1 {
		t.Fatalf("want one document body, got %d", len(bodies))
	}
	var sections []*xmlElement
	section := &xmlElement{Name: xml.Name{Local: "testSection"}}
	for _, child := range bodies[0].Children {
		section.Children = append(section.Children, child)
		if len(child.all("sectPr")) > 0 {
			sections = append(sections, section)
			section = &xmlElement{Name: xml.Name{Local: "testSection"}}
		}
	}
	if len(section.Children) > 0 {
		t.Fatal("document content follows its final section properties")
	}
	return sections
}

func bidStyledParagraphs(element *xmlElement, style string) []*xmlElement {
	var matches []*xmlElement
	for _, paragraph := range element.all("p") {
		styles := paragraph.all("pStyle")
		if len(styles) == 1 && styles[0].attribute("val") == style {
			matches = append(matches, paragraph)
		}
	}
	return matches
}

// The cached result must be inside the complex field. Word replaces this
// range when updating the TOC; placing a second ordinary contents list beside
// it would leave duplicated entries after an update.
func bidComplexFieldCache(t *testing.T, element *xmlElement, instruction string) string {
	t.Helper()
	state, fields, instructions := "", 0, 0
	var cache strings.Builder
	var visit func(*xmlElement)
	visit = func(node *xmlElement) {
		switch node.Name.Local {
		case "fldChar":
			switch node.attribute("fldCharType") {
			case "begin":
				if state != "" {
					t.Fatal("unexpected nested complex field")
				}
				state = "instruction"
				fields++
			case "separate":
				if state != "instruction" {
					t.Fatal("field result starts without its instruction")
				}
				state = "result"
			case "end":
				if state != "result" {
					t.Fatal("field ends without its result separator")
				}
				state = ""
			default:
				t.Fatalf("unknown complex field marker %q", node.attribute("fldCharType"))
			}
		case "instrText":
			if state != "instruction" || !strings.Contains(node.Text, instruction) {
				t.Fatalf("unexpected field instruction %q", node.Text)
			}
			instructions++
		case "t":
			if state == "result" {
				cache.WriteString(node.Text)
			}
		}
		for _, child := range node.Children {
			visit(child)
		}
	}
	visit(element)
	if state != "" || fields != 1 || instructions != 1 {
		t.Fatalf("want one complete %s field, got state=%q fields=%d instructions=%d", instruction, state, fields, instructions)
	}
	if len(element.all("fldSimple")) != 0 {
		t.Fatal("simple fields discard their cached result in the browser preview")
	}
	for _, run := range element.all("r") {
		if (len(run.all("fldChar")) > 0 || len(run.all("instrText")) > 0) && len(run.all("t")) > 0 {
			t.Fatal("cached text shares a field-control run and disappears in docx-preview")
		}
	}
	return cache.String()
}

func TestBuildBidDOCXCoverUsesSuppliedFactsAndPreservesUnknownFields(t *testing.T) {
	markdown := "# 投标文件\n\n正本\n\n" +
		"项目名称：测试仪器采购项目\n项目编号：TEST-2025-01\n" +
		"投标人名称：示例供应商（仅测试）\n法定代表人：张某（仅测试）\n投标日期：2025年10月1日\n\n" +
		"## 第一章 基本资料\n\n正文内容。"
	data, err := BuildBidDOCX("投标文件", markdown)
	if err != nil {
		t.Fatal(err)
	}
	sections := bidDocumentSections(t, wordParts(t, data)["word/document.xml"])
	if len(sections) != 3 {
		t.Fatalf("want cover, contents and body sections, got %d", len(sections))
	}
	cover := sections[0].documentText()
	for _, supplied := range []string{"正本", "测试仪器采购项目", "TEST-2025-01", "示例供应商（仅测试）", "张某（仅测试）", "2025年10月1日"} {
		if strings.Count(cover, supplied) != 1 || strings.Contains(sections[2].documentText(), supplied) {
			t.Errorf("supplied cover value %q was lost or repeated in the body", supplied)
		}
	}

	// An unfamiliar field and surrounding prose stay in the editable body;
	// they must not be interpreted as a replacement for a company fact.
	unknown := "联系人邮箱：[技术联系人](https://example.invalid/contact)\n" +
		"请依据采购要求填写以下资料，当前缺少投标人信息。\n\n## 第一章 基本资料\n\n正文内容。"
	data, err = BuildBidDOCX("投标文件", unknown)
	if err != nil {
		t.Fatal(err)
	}
	sections = bidDocumentSections(t, wordParts(t, data)["word/document.xml"])
	for _, placeholder := range []string{"[项目名称]", "[项目编号]", "[投标人名称]", "[签字或盖章]", "[日期]"} {
		if !strings.Contains(sections[0].documentText(), placeholder) {
			t.Errorf("missing information was not left as editable placeholder %q", placeholder)
		}
	}
	for _, retained := range []string{"联系人邮箱：", "技术联系人", "https://example.invalid/contact", "当前缺少投标人信息。"} {
		if !strings.Contains(sections[2].documentText(), retained) {
			t.Errorf("unknown front matter lost original text %q", retained)
		}
	}
	if strings.Contains(sections[0].documentText(), "技术联系人") {
		t.Fatal("unrecognized contact field was promoted into a supplied company fact")
	}
}

func TestBuildBidDOCXNativeContentsBookmarksAndNormalizedChapters(t *testing.T) {
	markdown := "# 投标文件\n\n## 目录\n\n" +
		"1. 第一章 基本资料\n2. 第二章 技术响应\n3. 第三章 待编制服务方案\n\n---\n\n" +
		"## 第一章 基本资料\n\n章正文。\n\n### 1.1 项目说明\n\n小节正文。\n\n" +
		"#### 1.1.1 实施范围\n\n正文。\n\n##### 详细条目\n\n正文。\n\n" +
		"## 第一章 基本资料（续）\n\n续写正文。\n\n## 第二章 技术响应\n\n技术正文。"
	data, err := BuildBidDOCX("投标文件", markdown)
	if err != nil {
		t.Fatal(err)
	}
	parts := wordParts(t, data)
	document := parts["word/document.xml"]
	sections := bidDocumentSections(t, document)
	cache := bidComplexFieldCache(t, sections[1], `TOC \o "1-3" \h \z \u`)
	for _, label := range []string{"第一章 基本资料", "第二章 技术响应", "第三章 待编制服务方案", "1.1 项目说明", "1.1.1 实施范围"} {
		// The continuation title contains the first chapter title too; count
		// actual paragraphs to distinguish navigation entries from substrings.
		count := 0
		for _, paragraph := range sections[1].all("p") {
			if paragraph.documentText() == label {
				count++
			}
		}
		if count != 1 || !strings.Contains(cache, label) {
			t.Errorf("want exactly one cached contents entry %q, got %d", label, count)
		}
	}
	if strings.Contains(cache, "详细条目") {
		t.Fatal("fourth-level heading must not appear in a 1-3 level TOC")
	}
	if strings.Contains(sections[2].documentText(), "第三章 待编制服务方案") {
		t.Fatal("planned contents entry created an invented empty chapter")
	}
	if len(bidStyledParagraphs(sections[2], "TOCHeading")) != 0 || len(bidStyledParagraphs(sections[1], "TOCHeading")) != 1 {
		t.Fatal("original Markdown contents survived as a second body contents list")
	}
	if len(sections[1].all("tab")) != 0 || strings.Contains(cache, "页") {
		t.Fatal("contents cache contains guessed page numbers")
	}
	for style, label := range map[string]string{
		"BidFirstChapter": "第一章 基本资料", "Heading1": "第二章 技术响应",
		"Heading3": "1.1.1 实施范围", "Heading4": "详细条目",
	} {
		paragraphs := bidStyledParagraphs(sections[2], style)
		if len(paragraphs) != 1 || paragraphs[0].documentText() != label {
			t.Errorf("Markdown heading was not normalized to %s for %q", style, label)
		}
	}
	secondLevel := bidStyledParagraphs(sections[2], "Heading2")
	if len(secondLevel) != 2 || secondLevel[0].documentText() != "1.1 项目说明" || secondLevel[1].documentText() != "第一章 基本资料（续）" {
		t.Fatal("subsections and continuation titles should stay below top-level chapters")
	}
	bookmarks := make(map[string]string)
	ids := make(map[string]bool)
	for _, bookmark := range sections[2].all("bookmarkStart") {
		name, id := bookmark.attribute("name"), bookmark.attribute("id")
		if name == "" || id == "" || bookmarks[name] != "" || ids[id] {
			t.Fatalf("heading bookmark is missing or duplicated: name=%q id=%q", name, id)
		}
		bookmarks[name], ids[id] = id, true
	}
	if len(bookmarks) != 6 || len(sections[2].all("bookmarkEnd")) != len(bookmarks) {
		t.Fatal("each original heading must retain one complete bookmark")
	}
	for _, hyperlink := range sections[1].all("hyperlink") {
		if bookmarks[hyperlink.attribute("anchor")] == "" {
			t.Errorf("TOC hyperlink points outside the generated body: %q", hyperlink.attribute("anchor"))
		}
	}
	if len(sections[1].all("hyperlink")) != 5 {
		t.Fatal("all rendered headings in the TOC must link to the body, excluding planned or fourth-level entries")
	}
	if len(parts["word/settings.xml"].all("updateFields")) != 1 || parts["word/settings.xml"].all("updateFields")[0].attribute("val") != "true" {
		t.Fatal("Word is not instructed to update native navigation fields")
	}
}

func TestBuildBidDOCXSectionNumberingAndIndependentHeadingStyles(t *testing.T) {
	data, err := BuildBidDOCX("投标文件", "## 第一章\n\n正文。\n\n### 小节\n\n正文。\n\n## 第二章\n\n正文。")
	if err != nil {
		t.Fatal(err)
	}
	parts := wordParts(t, data)
	sections := bidDocumentSections(t, parts["word/document.xml"])
	if len(sections) != 3 {
		t.Fatalf("want exactly three sections, got %d", len(sections))
	}
	for i, section := range sections {
		properties := section.all("sectPr")
		if len(properties) != 1 || len(properties[0].all("type")) != 1 || properties[0].all("type")[0].attribute("val") != "nextPage" {
			t.Errorf("section %d has an invalid page transition", i)
		}
		if i < 2 {
			if len(section.all("headerReference"))+len(section.all("footerReference"))+len(section.all("pgNumType")) != 0 {
				t.Errorf("front matter section %d inherits body page furniture", i)
			}
			continue
		}
		for _, kind := range []string{"headerReference", "footerReference"} {
			refs := section.all(kind)
			if len(refs) != 1 || refs[0].attribute("type") != "default" || refs[0].attribute("id") == "" {
				t.Errorf("body has no valid %s", kind)
			}
		}
		pageNumbers := section.all("pgNumType")
		if len(pageNumbers) != 1 || pageNumbers[0].attribute("start") != "1" || pageNumbers[0].attribute("fmt") != "decimal" {
			t.Fatal("body page numbering must restart at Arabic page 1")
		}
	}
	for _, name := range []string{"word/header1.xml", "word/footer1.xml", "word/settings.xml"} {
		if parts[name] == nil {
			t.Fatalf("missing native Word part %s", name)
		}
	}
	if parts["word/header1.xml"].documentText() != "投标文件" {
		t.Fatal("body header does not use the document title")
	}
	if strings.TrimSpace(bidComplexFieldCache(t, parts["word/footer1.xml"], "PAGE")) != "" {
		t.Fatal("footer cache must not repeat a fabricated page number on every browser page")
	}
	for _, br := range parts["word/document.xml"].all("br") {
		if br.attribute("type") == "page" {
			t.Fatal("explicit page breaks would duplicate next-page sections or chapter style breaks")
		}
	}
	styles := make(map[string]*xmlElement)
	for _, style := range parts["word/styles.xml"].all("style") {
		styles[style.attribute("styleId")] = style
	}
	if len(styles["Heading1"].all("pageBreakBefore")) != 1 || styles["Heading1"].all("pageBreakBefore")[0].attribute("val") == "0" {
		t.Fatal("subsequent chapters must start a new page")
	}
	for _, name := range []string{"BidFirstChapter", "Heading2", "Heading3", "Heading4"} {
		style := styles[name]
		if style == nil || len(style.all("pageBreakBefore")) != 1 || style.all("pageBreakBefore")[0].attribute("val") != "0" {
			t.Errorf("%s can cause an extra blank page or paginate every subsection", name)
		}
	}
	for _, name := range []string{"Heading2", "Heading3", "Heading4"} {
		if styles[name].all("basedOn")[0].attribute("val") != "Normal" {
			t.Errorf("%s must not inherit chapter page breaks", name)
		}
	}
	if len(styles["TOCHeading"].all("outlineLvl")) != 0 {
		t.Fatal("contents title can recursively enter its own TOC")
	}
	first := bidStyledParagraphs(sections[2], "BidFirstChapter")
	if len(first) != 1 || len(first[0].all("pageBreakBefore")) != 0 {
		t.Fatal("first chapter should use its no-break style after the contents section")
	}
}

func TestBuildBidDOCXTablesFitBindingMarginsAndPreserveOriginalText(t *testing.T) {
	longText := strings.Repeat("超长产品参数", 80)
	markdown := "## 技术响应\n\n[原始规格](https://example.invalid/spec?q=1&b=2)\n\n" +
		"| 序号 | 参数名称 | 响应说明 | 数量 | 单位 | 备注 | 证明文件 | 是否满足 |\n" +
		"| --- | --- | --- | ---: | --- | --- | --- | --- |\n" +
		"| 1 | **温度范围** | " + longText + " | 2 | 台 | 原文保留 | 用户提供 | 是 |\n" +
		"| 2 | 后续参数 | 原文说明 | 1 | 套 | 待补充 | 原始附件 | 否 |\n"
	data, err := BuildBidDOCX("投标文件", markdown)
	if err != nil {
		t.Fatal(err)
	}
	parts := wordParts(t, data)
	document := parts["word/document.xml"]
	sections := bidDocumentSections(t, document)
	for _, value := range []string{"原始规格", "https://example.invalid/spec?q=1&b=2", longText, "温度范围", "原始附件"} {
		if !strings.Contains(sections[2].documentText(), value) {
			t.Errorf("bid layout lost source content %q", value)
		}
	}
	tables := sections[2].all("tbl")
	if len(tables) != 1 {
		t.Fatalf("want one editable Word table, got %d", len(tables))
	}
	table := tables[0]
	if len(table.all("tblHeader")) != 1 || len(table.all("trHeight")) != 0 {
		t.Fatal("table must repeat its header without fixed row heights clipping content")
	}
	width := 0
	for _, column := range table.all("gridCol") {
		value, err := strconv.Atoi(column.attribute("w"))
		if err != nil || value <= 0 {
			t.Fatalf("invalid table width %q", column.attribute("w"))
		}
		width += value
	}
	margin := sections[2].all("pgMar")[0]
	left, _ := strconv.Atoi(margin.attribute("left"))
	right, _ := strconv.Atoi(margin.attribute("right"))
	page, _ := strconv.Atoi(sections[2].all("pgSz")[0].attribute("w"))
	if left <= right || width != page-left-right {
		t.Errorf("table width %d must fit the bound page's printable width %d with a larger left margin", width, page-left-right)
	}
	for _, row := range table.all("tr") {
		if len(row.all("tc")) != 8 {
			t.Fatal("bid layout changed the original table's column count")
		}
		for _, cell := range row.all("tc") {
			if len(cell.all("p")) != 1 || len(cell.all("noWrap")) != 1 || cell.all("noWrap")[0].attribute("val") != "0" {
				t.Fatal("native table text must remain editable and wrap within its cells")
			}
			styles := cell.all("pStyle")
			if len(styles) != 1 || styles[0].attribute("val") != "BidTable" {
				t.Fatal("table inherited the body's first-line indent")
			}
		}
	}
}

func TestBuildBidDOCXDeterministicFallbackAndEmptyBody(t *testing.T) {
	first, err := BuildBidDOCX("文档", "无章节正文，保持原文。")
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildBidDOCX("文档", "无章节正文，保持原文。")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("same bid content creates different artifact bytes")
	}
	document := wordParts(t, first)["word/document.xml"]
	sections := bidDocumentSections(t, document)
	if len(bidStyledParagraphs(sections[0], "Title")) != 1 || bidStyledParagraphs(sections[0], "Title")[0].documentText() != "投标文件" {
		t.Fatal("generic title does not use the bid title fallback")
	}
	if !strings.Contains(sections[2].documentText(), "无章节正文，保持原文。") || len(bidStyledParagraphs(sections[2], "BidFirstChapter")) != 1 {
		t.Fatal("unstructured source body was lost or lacks a formal body start")
	}
	for _, content := range []string{"", " \n\t ", "\x00\x01\ufffe"} {
		if _, err := BuildBidDOCX("投标文件", content); !errors.Is(err, ErrEmptyContent) {
			t.Errorf("empty bid body returned %v", err)
		}
	}
}

func TestBuildBidDOCXManualLinesAndSignatureFieldsDoNotStretch(t *testing.T) {
	cases := []struct {
		name, source, wantStyle string
	}{
		{"soft line break", "公司简介：\n这段原文在同一段落内换行。", "BidBodyLines"},
		{"hard line break", "人员结构：  \n这段原文在同一段落内换行。", "BidBodyLines"},
		{"escaped hard line break", "经营业绩：\\\n这段原文在同一段落内换行。", "BidBodyLines"},
		{"HTML break", "公司简介：<br>这段原文在同一段落内换行。", "BidBodyLines"},
		{"HTML self-closing break", "人员结构：<br/>这段原文在同一段落内换行。", "BidBodyLines"},
		{"HTML spaced break", "经营业绩：<br />这段原文在同一段落内换行。", "BidBodyLines"},
		{"signature with ASCII parentheses", "供应商(公章)：[供应商全称]  \n法定代表人（签字或盖章）：  \n日期：[日期]", "BidForm"},
		{"signature with Chinese parentheses", "供应商（公章）：[供应商全称]  \n被授权人（签字或盖章）：  \n日期：[日期]", "BidForm"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			data, err := BuildBidDOCX("投标文件", "## 第一章\n\n"+test.source+"\n\n这里是正常正文，继续使用两端对齐的标书正文样式。")
			if err != nil {
				t.Fatal(err)
			}
			parts := wordParts(t, data)
			sections := bidDocumentSections(t, parts["word/document.xml"])
			paragraphs := bidStyledParagraphs(sections[2], test.wantStyle)
			if len(paragraphs) != 1 || len(paragraphs[0].all("br")) == 0 {
				t.Fatalf("manual or form lines must use one %s paragraph with retained line breaks", test.wantStyle)
			}
			if len(bidStyledParagraphs(sections[2], "BidBody")) != 1 {
				t.Fatal("manual lines incorrectly use a stretched body paragraph, or ordinary prose lost its body style")
			}
			var layout *xmlElement
			for _, style := range parts["word/styles.xml"].all("style") {
				if style.attribute("styleId") == test.wantStyle {
					layout = style
					break
				}
			}
			if layout == nil || len(layout.all("jc")) != 1 || layout.all("jc")[0].attribute("val") != "left" {
				t.Fatal("manual and signature lines must override body justification with left alignment")
			}
		})
	}
}
