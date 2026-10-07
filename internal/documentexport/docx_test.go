package documentexport

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
)

type xmlElement struct {
	Name     xml.Name
	Attrs    []xml.Attr
	Text     string
	Children []*xmlElement
}

func parsePart(t *testing.T, data string) *xmlElement {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(data))
	var root *xmlElement
	var stack []*xmlElement
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("invalid package XML: %v", err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			element := &xmlElement{Name: token.Name, Attrs: token.Attr}
			if len(stack) > 0 {
				stack[len(stack)-1].Children = append(stack[len(stack)-1].Children, element)
			} else {
				root = element
			}
			stack = append(stack, element)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(token)
			}
		}
	}
	if root == nil {
		t.Fatal("empty package XML")
	}
	return root
}

func (element *xmlElement) all(name string) []*xmlElement {
	var matches []*xmlElement
	if element.Name.Local == name {
		matches = append(matches, element)
	}
	for _, child := range element.Children {
		matches = append(matches, child.all(name)...)
	}
	return matches
}

func (element *xmlElement) attribute(name string) string {
	for _, attribute := range element.Attrs {
		if attribute.Name.Local == name {
			return attribute.Value
		}
	}
	return ""
}

func (element *xmlElement) documentText() string {
	var result strings.Builder
	for _, text := range element.all("t") {
		result.WriteString(text.Text)
	}
	return result.String()
}

func wordParts(t *testing.T, data []byte) map[string]*xmlElement {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("invalid Word ZIP: %v", err)
	}
	parts := make(map[string]*xmlElement)
	for _, entry := range reader.File {
		stream, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(stream)
		_ = stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		parts[entry.Name] = parsePart(t, string(content))
		if strings.Contains(entry.Name, "vbaProject") || strings.Contains(entry.Name, "altChunk") || strings.Contains(entry.Name, "media/") {
			t.Fatalf("unexpected executable or externally fetched part %q", entry.Name)
		}
	}
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml", "word/styles.xml", "word/numbering.xml", "word/_rels/document.xml.rels"} {
		if parts[name] == nil {
			t.Fatalf("missing required Word package part %q", name)
		}
	}
	return parts
}

func TestBuildDOCXNativeContentAndSafety(t *testing.T) {
	markdown := "# 第一章 项目响应\n\n**技术响应**及*实施方案*，支持 &amp; 与 \\* 转义。\n\n" +
		"## 1.1 项目说明\n\n### 三级标题\n\n#### 四级标题\n\n" +
		"7. 第一项\n8. 第二项\n   - 子项\n\n普通正文\n\n1. 重新起始\n\n" +
		"[官网](https://example.invalid/x?q=1&b=2)\n\n![资质扫描件](https://example.invalid/private.png)\n\n" +
		"```text\n<literal>& 原文\n第二行\t缩进\n```\n\n" +
		"恶意字符串 <w:injected/> 与控制符 \x00\x01\ufffe\uffff，正文末尾。"
	title := `投标文件 & <w:injected/> "报价"`
	data, err := BuildDOCX(title, markdown)
	if err != nil {
		t.Fatal(err)
	}
	parts := wordParts(t, data)
	document := parts["word/document.xml"]
	if document.Name.Space != wordNamespace {
		t.Fatal("document lacks the WordprocessingML namespace")
	}
	if len(document.all("injected")) != 0 {
		t.Fatal("source text injected an XML element")
	}
	plain := document.documentText()
	for _, expected := range []string{title, "技术响应", "实施方案", "支持 & 与 * 转义", "第一项", "第二项", "子项", "重新起始", "https://example.invalid/x?q=1&b=2", "[图片：资质扫描件（需补充原图）]", "<literal>& 原文", "正文末尾。"} {
		if !strings.Contains(plain, expected) {
			t.Errorf("missing readable content %q", expected)
		}
	}
	for _, invalid := range []rune{0, 1, 0xfffe, 0xffff} {
		if strings.ContainsRune(plain, invalid) {
			t.Errorf("XML contains invalid character U+%04X", invalid)
		}
	}
	if len(document.all("b")) == 0 || len(document.all("i")) == 0 {
		t.Fatal("bold or italic was flattened into text")
	}
	if len(document.all("br")) == 0 || len(document.all("tab")) == 0 {
		t.Fatal("code line breaks or indentation were lost")
	}
	styles := make(map[string]bool)
	for _, paragraphStyle := range document.all("pStyle") {
		styles[paragraphStyle.attribute("val")] = true
	}
	for _, style := range []string{"Title", "Heading1", "Heading2", "Heading3", "Heading4"} {
		if !styles[style] {
			t.Errorf("heading lost native style %s", style)
		}
	}
	numbering := parts["word/numbering.xml"]
	definitions := numbering.all("num")
	if len(definitions) != 3 {
		t.Fatalf("want separate ordered, nested, and restarted list definitions, got %d", len(definitions))
	}
	if start := definitions[0].all("startOverride")[0].attribute("val"); start != "7" {
		t.Errorf("ordered list starts at %s instead of 7", start)
	}
	if start := definitions[2].all("startOverride")[0].attribute("val"); start != "1" {
		t.Errorf("new list starts at %s instead of 1", start)
	}
	if len(document.all("numPr")) != 4 {
		t.Errorf("list items are not native Word numbered paragraphs")
	}
	for name, part := range parts {
		for _, relationship := range part.all("Relationship") {
			if relationship.attribute("TargetMode") == "External" {
				t.Errorf("external Word relationship in %s", name)
			}
		}
	}
}

func TestBuildDOCXTablesStayEditableWithinPage(t *testing.T) {
	longWord := strings.Repeat("parameterWITHOUTspaces", 150)
	markdown := "| 参数 | 型号 | 数量 | 单位 | 厂商 | 证明 | 备注 | 链接 |\n" +
		"| :--- | :---: | ---: | --- | --- | --- | --- | --- |\n" +
		"| **温度范围** | " + longWord + " | 2 | 台 | 厂商原文 | 待补充 | -20°C~40°C | [规格](https://example.invalid) |\n" +
		"| 后续行 | 示例 | 1 | 套 | 实际厂商 | 用户提供 | 中文说明 | 保留文字 |\n"
	data, err := BuildDOCX("参数响应表", markdown)
	if err != nil {
		t.Fatal(err)
	}
	parts := wordParts(t, data)
	document := parts["word/document.xml"]
	tables := document.all("tbl")
	if len(tables) != 1 {
		t.Fatalf("want native table, got %d", len(tables))
	}
	table := tables[0]
	rows := table.all("tr")
	if len(rows) != 3 {
		t.Fatalf("want header and 2 body rows, got %d", len(rows))
	}
	if len(rows[0].all("tblHeader")) != 1 || len(rows[1].all("tblHeader")) != 0 {
		t.Fatal("only the header row must repeat on following pages")
	}
	if len(table.all("trHeight")) != 0 {
		t.Fatal("fixed table row height can clip long text")
	}
	width := 0
	for _, column := range table.all("gridCol") {
		columnWidth, err := strconv.Atoi(column.attribute("w"))
		if err != nil || columnWidth <= 0 {
			t.Fatalf("invalid table column width %q", column.attribute("w"))
		}
		width += columnWidth
	}
	page := document.all("pgSz")[0]
	margin := document.all("pgMar")[0]
	pageW, _ := strconv.Atoi(page.attribute("w"))
	left, _ := strconv.Atoi(margin.attribute("left"))
	right, _ := strconv.Atoi(margin.attribute("right"))
	if width != pageW-left-right {
		t.Errorf("table width %d does not fit printable page width %d", width, pageW-left-right)
	}
	for _, row := range rows {
		cells := row.all("tc")
		if len(cells) != 8 {
			t.Errorf("row contains %d cells, want 8", len(cells))
		}
		rowWidth := 0
		for _, cell := range cells {
			cellWidth, _ := strconv.Atoi(cell.all("tcW")[0].attribute("w"))
			rowWidth += cellWidth
			if len(cell.all("p")) == 0 {
				t.Fatal("table cell lacks required native paragraph")
			}
			if cell.all("noWrap")[0].attribute("val") != "0" {
				t.Fatal("table cells must wrap long words")
			}
		}
		if rowWidth != width {
			t.Errorf("row width %d differs from grid width %d", rowWidth, width)
		}
	}
	if !strings.Contains(table.documentText(), longWord) {
		t.Fatal("long product parameter was truncated")
	}
	for _, size := range table.all("sz") {
		if size.attribute("val") != "18" {
			t.Errorf("wide table does not use readable 9-point text: %s", size.attribute("val"))
		}
	}
	if table.all("tblLayout")[0].attribute("type") != "fixed" {
		t.Fatal("table autofit may grow beyond printable page")
	}
}

func TestBuildDOCXDeterministicAndRejectsEmptyBody(t *testing.T) {
	first, err := BuildDOCX("同一份标书", "# 章节\n\n正文。\n\n- 一\n- 二")
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildDOCX("同一份标书", "# 章节\n\n正文。\n\n- 一\n- 二")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("same content creates different artifact bytes")
	}
	for _, body := range []string{"", " \n\t ", "\x00\x01\ufffe"} {
		if _, err := BuildDOCX("只有标题", body); !errors.Is(err, ErrEmptyContent) {
			t.Errorf("empty body returned %v", err)
		}
	}
}

func TestBuildDOCXPromotesOnlyMatchingFirstH1ToTitle(t *testing.T) {
	cases := []struct {
		name, markdown      string
		wantTitles, wantH1s int
	}{
		{"plain title", "# 投标文件\n\n正文。", 1, 0},
		{"formatted title", "# **投标**文件\n\n正文。", 1, 0},
		{"later same heading retained", "# 投标文件\n\n正文。\n\n# 投标文件", 2, 1},
		{"different first heading retained", "# 技术响应\n\n正文。", 1, 1},
		{"nonfirst matching heading retained", "正文。\n\n# 投标文件", 2, 1},
		{"link address retained", "# [投标文件](https://example.invalid/spec)\n\n正文。", 2, 1},
		{"image placeholder retained", "# ![投标文件](https://example.invalid/seal.png)\n\n正文。", 2, 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			data, err := BuildDOCX("投标文件", test.markdown)
			if err != nil {
				t.Fatal(err)
			}
			document := wordParts(t, data)["word/document.xml"]
			if count := strings.Count(document.documentText(), "投标文件"); count != test.wantTitles {
				t.Errorf("readable title appears %d times, want %d", count, test.wantTitles)
			}
			headingCount, nativeTitleCount := 0, 0
			for _, style := range document.all("pStyle") {
				if style.attribute("val") == "Heading1" {
					headingCount++
				}
				if style.attribute("val") == "Title" {
					nativeTitleCount++
				}
			}
			if headingCount != test.wantH1s {
				t.Errorf("have %d native H1 paragraphs, want %d", headingCount, test.wantH1s)
			}
			if nativeTitleCount != 1 {
				t.Errorf("have %d native Title paragraphs, want 1", nativeTitleCount)
			}
			if !strings.Contains(document.documentText(), "正文。") {
				t.Error("body was lost when promoting the title")
			}
			if test.name == "link address retained" && !strings.Contains(document.documentText(), "https://example.invalid/spec") {
				t.Error("heading link address was lost")
			}
			if test.name == "image placeholder retained" && !strings.Contains(document.documentText(), "需补充原图") {
				t.Error("heading image placeholder was lost")
			}
		})
	}
}
