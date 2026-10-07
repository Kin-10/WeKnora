// Package documentexport creates editable Word documents from Markdown without
// fetching external resources or invoking an office application.
package documentexport

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/bidformat"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

const (
	// Dimensions are twentieths of a point (twips). A4 with 1-inch margins.
	pageWidth      = 11906
	pageHeight     = 16838
	pageMargin     = 1440
	printableWidth = pageWidth - 2*pageMargin
	wordNamespace  = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
)

// ErrEmptyContent is returned when there is no document body to export.
var ErrEmptyContent = errors.New("document body is empty")

// BuildDOCX converts Markdown into native, editable Word paragraphs and tables.
// Links are readable text, and images become explicit placeholders; it never
// reads local paths, downloads URLs, embeds macros, or imports HTML into Word.
// Application-specific citation markers should be resolved by the caller first.
func BuildDOCX(title, markdown string) ([]byte, error) {
	markdown = cleanXMLText(markdown)
	if strings.TrimSpace(markdown) == "" {
		return nil, ErrEmptyContent
	}
	source := []byte(markdown)
	parser := goldmark.New(goldmark.WithExtensions(extension.GFM))
	doc := parser.Parser().Parse(text.NewReader(source))
	w := &documentWriter{source: source}
	w.body.WriteString(xmlDeclaration + `<w:document xmlns:w="` + wordNamespace + `"><w:body>`)
	if title = strings.TrimSpace(cleanXMLText(title)); title != "" {
		w.paragraphRaw(title, paragraphOptions{style: "Title"}, runOptions{})
		// The caller can derive the document title from the first Markdown H1.
		// Promote that heading to the native Title paragraph once, while retaining
		// every later heading and any first heading containing extra resources.
		if first, ok := doc.FirstChild().(*ast.Heading); ok && first.Level == 1 &&
			strings.TrimSpace(w.inlineText(first)) == title && isPlainHeading(first) {
			doc.RemoveChild(doc, first)
		}
	}
	w.blocks(doc, paragraphOptions{})
	fmt.Fprintf(&w.body, `<w:sectPr><w:pgSz w:w="%d" w:h="%d"/><w:pgMar w:top="%d" w:right="%d" w:bottom="%d" w:left="%d" w:header="720" w:footer="720" w:gutter="0"/></w:sectPr></w:body></w:document>`, pageWidth, pageHeight, pageMargin, pageMargin, pageMargin, pageMargin)

	parts := []docxPart{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", rootRelationshipsXML},
		{"word/document.xml", w.body.String()},
		{"word/styles.xml", stylesXML},
		{"word/numbering.xml", w.numberingXML()},
		{"word/_rels/document.xml.rels", documentRelationshipsXML},
		{"docProps/core.xml", xmlDeclaration + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>` + escapeXML(title) + `</dc:title><dc:creator>WeKnora</dc:creator></cp:coreProperties>`},
		{"docProps/app.xml", xmlDeclaration + `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Application>WeKnora</Application></Properties>`},
	}
	return writeDOCXParts(parts)
}

type docxPart struct{ name, content string }

func writeDOCXParts(parts []docxPart) ([]byte, error) {
	var output bytes.Buffer
	z := zip.NewWriter(&output)
	for _, part := range parts {
		header := &zip.FileHeader{Name: part.name, Method: zip.Deflate}
		header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
		entry, err := z.CreateHeader(header)
		if err != nil {
			return nil, fmt.Errorf("create Word package part: %w", err)
		}
		if _, err := entry.Write([]byte(part.content)); err != nil {
			return nil, fmt.Errorf("write Word package part: %w", err)
		}
	}
	if err := z.Close(); err != nil {
		return nil, fmt.Errorf("finish Word package: %w", err)
	}
	return output.Bytes(), nil
}

type documentWriter struct {
	source      []byte
	body        strings.Builder
	lists       []listDefinition
	bid         *bidLayout
	format      *bidformat.Spec
	activeStyle string
	err         error
}

type paragraphOptions struct {
	style    string
	align    string
	indent   int
	numID    int
	level    int
	fontSize int
	bold     bool
	bookmark int
}

type runOptions struct {
	bold, italic, strike, code bool
	fontSize                   int
}

type listDefinition struct {
	ordered      bool
	start, level int
}

func isPlainHeading(heading *ast.Heading) bool {
	plain := true
	_ = ast.Walk(heading, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node.(type) {
		case *ast.Image, *ast.Link, *ast.AutoLink, *ast.RawHTML:
			plain = false
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return plain
}

func (w *documentWriter) blocks(parent ast.Node, options paragraphOptions) {
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		w.block(node, options)
	}
}

func (w *documentWriter) block(node ast.Node, options paragraphOptions) {
	switch n := node.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		if w.bid != nil && options.style == "" {
			options.style = "BidBody"
			if options.numID > 0 || options.indent > 0 {
				options.style = "BidList"
			} else if isBidFormParagraph(w.inlineText(node)) {
				options.style = "BidForm"
			} else if hasBidManualLineBreak(node, w.source) {
				options.style = "BidBodyLines"
			}
		}
		w.paragraph(node, options)
	case *ast.Heading:
		if w.bid != nil {
			w.bidHeading(n, options)
			return
		}
		options.style = fmt.Sprintf("Heading%d", min(n.Level, 4))
		w.paragraph(node, options)
	case *ast.List:
		w.list(n, options)
	case *ast.Blockquote:
		options.indent += 360
		w.blocks(n, options)
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		var code strings.Builder
		for i := 0; i < node.Lines().Len(); i++ {
			line := node.Lines().At(i)
			code.Write(line.Value(w.source))
		}
		w.paragraphRaw(strings.TrimSuffix(code.String(), "\n"), options, runOptions{code: true, fontSize: 20})
	case *extast.Table:
		w.table(n, options)
	case *ast.ThematicBreak:
		if w.bid != nil {
			return // Formal sections provide the separation in the bid template.
		}
		// Preserve the separator as an editable native paragraph border.
		w.body.WriteString(`<w:p><w:pPr><w:pBdr><w:bottom w:val="single" w:sz="4" w:space="1" w:color="808080"/></w:pBdr></w:pPr></w:p>`)
	case *ast.HTMLBlock:
		w.paragraphRaw(string(n.Text(w.source)), options, runOptions{})
	default:
		w.blocks(node, options)
	}
}

func (w *documentWriter) list(list *ast.List, options paragraphOptions) {
	level := min(options.level, 8)
	start := max(list.Start, 1)
	w.lists = append(w.lists, listDefinition{ordered: list.IsOrdered(), start: start, level: level})
	numID := len(w.lists)
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		firstParagraph := true
		for child := item.FirstChild(); child != nil; child = child.NextSibling() {
			childOptions := options
			childOptions.indent = max(options.indent, (level+1)*360)
			childOptions.numID = 0
			if _, nested := child.(*ast.List); nested {
				childOptions.level = level + 1
				w.block(child, childOptions)
				continue
			}
			if firstParagraph && (child.Kind() == ast.KindParagraph || child.Kind() == ast.KindTextBlock || child.Kind() == ast.KindHeading) {
				childOptions.numID, childOptions.level = numID, level
				firstParagraph = false
			}
			w.block(child, childOptions)
		}
	}
}

func (w *documentWriter) paragraphStart(options paragraphOptions) {
	w.activeStyle = options.style
	w.body.WriteString(`<w:p><w:pPr>`)
	if options.style != "" {
		w.body.WriteString(`<w:pStyle w:val="` + escapeXML(options.style) + `"/>`)
	}
	if options.numID > 0 {
		fmt.Fprintf(&w.body, `<w:numPr><w:ilvl w:val="%d"/><w:numId w:val="%d"/></w:numPr>`, options.level, options.numID)
	}
	w.body.WriteString(`<w:wordWrap w:val="1"/>`)
	rule := w.paragraphRule(options.style)
	if options.indent > 0 && options.numID == 0 && rule.FirstLineChars == nil {
		fmt.Fprintf(&w.body, `<w:ind w:left="%d"/>`, options.indent)
	}
	if options.align != "" && rule.Alignment == nil {
		w.body.WriteString(`<w:jc w:val="` + escapeXML(options.align) + `"/>`)
	}
	w.body.WriteString(ruleParagraphProperties(rule))
	w.body.WriteString(`</w:pPr>`)
}

func (w *documentWriter) paragraph(node ast.Node, options paragraphOptions) {
	w.paragraphStart(options)
	if options.bookmark > 0 {
		fmt.Fprintf(&w.body, `<w:bookmarkStart w:id="%d" w:name="_BidHeading%d"/>`, options.bookmark, options.bookmark)
	}
	w.inlines(node, runOptions{bold: options.bold, fontSize: options.fontSize})
	if options.bookmark > 0 {
		fmt.Fprintf(&w.body, `<w:bookmarkEnd w:id="%d"/>`, options.bookmark)
	}
	w.body.WriteString(`</w:p>`)
}

func (w *documentWriter) paragraphRaw(content string, options paragraphOptions, run runOptions) {
	w.paragraphStart(options)
	w.run(content, run)
	w.body.WriteString(`</w:p>`)
}

func markdownText(content []byte, raw bool) string {
	if raw {
		return string(content)
	}
	return html.UnescapeString(string(util.UnescapePunctuations(content)))
}

func (w *documentWriter) inlines(parent ast.Node, options runOptions) {
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		switch n := child.(type) {
		case *ast.Text:
			w.run(markdownText(n.Value(w.source), n.IsRaw()), options)
			if n.HardLineBreak() {
				w.run("\n", options)
			} else if n.SoftLineBreak() {
				if w.bid != nil {
					w.run("\n", options)
				} else {
					w.run(" ", options)
				}
			}
		case *ast.String:
			w.run(markdownText(n.Value, n.IsRaw()), options)
		case *ast.Emphasis:
			childOptions := options
			if n.Level == 2 {
				childOptions.bold = true
			} else {
				childOptions.italic = true
			}
			w.inlines(n, childOptions)
		case *extast.Strikethrough:
			childOptions := options
			childOptions.strike = true
			w.inlines(n, childOptions)
		case *ast.CodeSpan:
			childOptions := options
			childOptions.code = true
			for segment := n.FirstChild(); segment != nil; segment = segment.NextSibling() {
				if textNode, ok := segment.(*ast.Text); ok {
					w.run(strings.ReplaceAll(string(textNode.Value(w.source)), "\n", " "), childOptions)
				}
			}
		case *ast.Link:
			w.inlines(n, options)
			if destination := markdownText(n.Destination, false); destination != "" && destination != w.inlineText(n) {
				w.run(" ("+destination+")", options)
			}
		case *ast.AutoLink:
			w.run(string(n.Label(w.source)), options)
		case *ast.Image:
			alt := strings.TrimSpace(w.inlineText(n))
			if alt == "" {
				alt = "待补充"
			}
			w.run("[图片："+alt+"（需补充原图）]", options)
		case *ast.RawHTML:
			raw := string(n.Text(w.source))
			if strings.EqualFold(raw, "<br>") || strings.EqualFold(raw, "<br/>") || strings.EqualFold(raw, "<br />") {
				w.run("\n", options)
			} else {
				w.run(raw, options)
			}
		case *extast.TaskCheckBox:
			if n.IsChecked {
				w.run("☑ ", options)
			} else {
				w.run("☐ ", options)
			}
		default:
			w.inlines(child, options)
		}
	}
}

// inlineText is used for sizing and labels, not for generating markup.
func (w *documentWriter) inlineText(node ast.Node) string {
	var result strings.Builder
	_ = ast.Walk(node, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := child.(type) {
		case *ast.Text:
			result.WriteString(markdownText(n.Value(w.source), n.IsRaw()))
		case *ast.String:
			result.WriteString(markdownText(n.Value, n.IsRaw()))
		case *ast.AutoLink:
			result.Write(n.Label(w.source))
		}
		return ast.WalkContinue, nil
	})
	return result.String()
}

func (w *documentWriter) run(content string, options runOptions) {
	if content == "" {
		return
	}
	w.body.WriteString(`<w:r><w:rPr>`)
	rule := w.paragraphRule(w.activeStyle)
	if options.code && rule.FontFamily == nil {
		w.body.WriteString(`<w:rFonts w:ascii="Courier New" w:hAnsi="Courier New" w:eastAsia="宋体"/>`)
	}
	if options.bold && rule.Bold == nil {
		w.body.WriteString(`<w:b/><w:bCs/>`)
	}
	if options.italic && rule.Italic == nil {
		w.body.WriteString(`<w:i/><w:iCs/>`)
	}
	if options.strike && !w.anonymousFormat() {
		w.body.WriteString(`<w:strike/>`)
	}
	if options.fontSize > 0 && rule.FontSizeHalfPoints == nil {
		fmt.Fprintf(&w.body, `<w:sz w:val="%d"/><w:szCs w:val="%d"/>`, options.fontSize, options.fontSize)
	}
	w.body.WriteString(ruleRunProperties(rule))
	w.body.WriteString(`</w:rPr>`)
	for _, item := range splitText(content) {
		switch item {
		case "\n":
			w.body.WriteString(`<w:br/>`)
		case "\t":
			w.body.WriteString(`<w:tab/>`)
		default:
			w.body.WriteString(`<w:t xml:space="preserve">` + escapeXML(item) + `</w:t>`)
		}
	}
	w.body.WriteString(`</w:r>`)
}

func splitText(value string) []string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	var pieces []string
	start := 0
	for index, character := range value {
		if character != '\n' && character != '\t' {
			continue
		}
		if index > start {
			pieces = append(pieces, value[start:index])
		}
		pieces = append(pieces, string(character))
		start = index + 1
	}
	if start < len(value) {
		pieces = append(pieces, value[start:])
	}
	return pieces
}

func (w *documentWriter) table(table *extast.Table, options paragraphOptions) {
	columns := len(table.Alignments)
	if columns == 0 {
		return
	}
	contentWidth := printableWidth
	if w.bid != nil {
		contentWidth = bidPrintableWidth
	}
	if w.format != nil {
		contentWidth = specPrintableWidth(*w.format)
	}
	availableWidth := max(contentWidth-min(options.indent, contentWidth/2), contentWidth/2)
	weights := make([]int, columns)
	for row := table.FirstChild(); row != nil; row = row.NextSibling() {
		column := 0
		for cell := row.FirstChild(); cell != nil && column < columns; cell = cell.NextSibling() {
			weights[column] = max(weights[column], min(max(displayWidth(w.inlineText(cell)), 12), 48))
			column++
		}
	}
	widths := columnWidths(weights, availableWidth)
	fontSize := 22
	if columns > 4 {
		fontSize = 20
	}
	if columns > 6 {
		fontSize = 18
	}
	tableRule := w.paragraphRule("BidTable")
	if tableRule.FontSizeHalfPoints != nil {
		fontSize = *tableRule.FontSizeHalfPoints
		if columns*minimumRuleCellWidth(tableRule) > availableWidth {
			w.err = fmt.Errorf("%w: %d 列表格无法在指定字号及缩进下保持可读宽度，请按招标要求拆表或提供横向页面模板", ErrUnsupportedTenderFormat, columns)
			return
		}
		widths = columnWidthsWithMinimum(weights, availableWidth, minimumRuleCellWidth(tableRule))
	}
	fmt.Fprintf(&w.body, `<w:tbl><w:tblPr><w:tblW w:w="%d" w:type="dxa"/><w:tblBorders>`, availableWidth)
	for _, edge := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
		color := "808080"
		if w.bid != nil {
			color = "D9D9D9"
		}
		if w.anonymousFormat() {
			color = "000000"
		}
		fmt.Fprintf(&w.body, `<w:%s w:val="single" w:sz="4" w:color="%s"/>`, edge, color)
	}
	w.body.WriteString(`</w:tblBorders><w:tblLayout w:type="fixed"/><w:tblCellMar><w:top w:w="80" w:type="dxa"/><w:left w:w="80" w:type="dxa"/><w:bottom w:w="80" w:type="dxa"/><w:right w:w="80" w:type="dxa"/></w:tblCellMar></w:tblPr><w:tblGrid>`)
	for _, width := range widths {
		fmt.Fprintf(&w.body, `<w:gridCol w:w="%d"/>`, width)
	}
	w.body.WriteString(`</w:tblGrid>`)
	for row := table.FirstChild(); row != nil; row = row.NextSibling() {
		_, header := row.(*extast.TableHeader)
		w.body.WriteString(`<w:tr>`)
		if header {
			w.body.WriteString(`<w:trPr><w:tblHeader/></w:trPr>`)
		}
		cell := row.FirstChild()
		for column := 0; column < columns; column++ {
			fmt.Fprintf(&w.body, `<w:tc><w:tcPr><w:tcW w:w="%d" w:type="dxa"/>`, widths[column])
			if header && !w.anonymousFormat() && (tableRule.Shading == nil || *tableRule.Shading) {
				w.body.WriteString(`<w:shd w:val="clear" w:fill="F2F2F2"/>`)
			}
			w.body.WriteString(`<w:noWrap w:val="0"/><w:vAlign w:val="center"/></w:tcPr>`)
			cellOptions := paragraphOptions{fontSize: fontSize, bold: header}
			if w.bid != nil {
				cellOptions.style = "BidTable"
				if header || w.bidCompactTableColumn(table, column) {
					cellOptions.align = "center"
				}
			}
			if column < len(table.Alignments) {
				switch table.Alignments[column] {
				case extast.AlignCenter:
					cellOptions.align = "center"
				case extast.AlignRight:
					cellOptions.align = "right"
				}
			}
			if cell == nil {
				w.paragraphRaw("", cellOptions, runOptions{})
			} else {
				w.paragraph(cell, cellOptions)
				cell = cell.NextSibling()
			}
			w.body.WriteString(`</w:tc>`)
		}
		w.body.WriteString(`</w:tr>`)
	}
	w.body.WriteString(`</w:tbl>`)
	// The ordinary layout uses an empty paragraph to separate a table from
	// following content. In a tender with exact line spacing that paragraph
	// occupies a full, otherwise unrequested line. The table cells already
	// contain their required paragraphs; no body spacer is needed for validity.
	suppressSpacer := w.anonymousFormat()
	if w.format != nil {
		bodyRule := w.paragraphRule("BidBody")
		suppressSpacer = suppressSpacer || w.format.Scope == bidformat.ScopeTechnical ||
			(bodyRule.SpaceBeforeTwips != nil && *bodyRule.SpaceBeforeTwips == 0 && bodyRule.SpaceAfterTwips != nil && *bodyRule.SpaceAfterTwips == 0) ||
			(w.format.Structure.BlankPages != nil && !*w.format.Structure.BlankPages)
	}
	if !suppressSpacer {
		w.body.WriteString(`<w:p/>`)
	}
}

func displayWidth(value string) int {
	width := 0
	for _, character := range value {
		if character > 0xff {
			width += 2
		} else {
			width++
		}
	}
	return width
}

func columnWidths(weights []int, total int) []int {
	sum := 0
	for i := range weights {
		weights[i] = max(weights[i], 12)
		sum += weights[i]
	}
	widths := make([]int, len(weights))
	used := 0
	for i, weight := range weights {
		widths[i] = max(total*weight/sum, 1)
		used += widths[i]
	}
	// Assign integer rounding to the last column to fit the printable area exactly.
	widths[len(widths)-1] += total - used
	return widths
}

func (w *documentWriter) numberingXML() string {
	var result strings.Builder
	result.WriteString(xmlDeclaration + `<w:numbering xmlns:w="` + wordNamespace + `">`)
	for abstractID := 0; abstractID < 2; abstractID++ {
		fmt.Fprintf(&result, `<w:abstractNum w:abstractNumId="%d"><w:multiLevelType w:val="multilevel"/>`, abstractID)
		for level := 0; level < 9; level++ {
			format, label := "bullet", "•"
			if abstractID == 1 {
				format, label = "decimal", fmt.Sprintf("%%%d.", level+1)
			}
			fmt.Fprintf(&result, `<w:lvl w:ilvl="%d"><w:start w:val="1"/><w:numFmt w:val="%s"/><w:lvlText w:val="%s"/><w:lvlJc w:val="left"/><w:pPr><w:tabs><w:tab w:val="num" w:pos="%d"/></w:tabs>`, level, format, label, (level+1)*360)
			rule := w.paragraphRule("BidList")
			if rule.FirstLineChars == nil {
				fmt.Fprintf(&result, `<w:ind w:left="%d" w:hanging="180"/>`, (level+1)*360)
			}
			result.WriteString(ruleParagraphProperties(rule) + `</w:pPr>`)
			if w.format != nil {
				result.WriteString(`<w:rPr>` + ruleRunProperties(rule) + `</w:rPr>`)
			}
			result.WriteString(`</w:lvl>`)
		}
		result.WriteString(`</w:abstractNum>`)
	}
	for index, list := range w.lists {
		abstractID := 0
		if list.ordered {
			abstractID = 1
		}
		fmt.Fprintf(&result, `<w:num w:numId="%d"><w:abstractNumId w:val="%d"/><w:lvlOverride w:ilvl="%d"><w:startOverride w:val="%d"/></w:lvlOverride></w:num>`, index+1, abstractID, list.level, list.start)
	}
	result.WriteString(`</w:numbering>`)
	return result.String()
}

// XML 1.0 permits tabs, newlines and carriage returns but not other C0 controls,
// unpaired surrogates, or U+FFFE/U+FFFF. Remove these before XML escaping.
func cleanXMLText(value string) string {
	return strings.Map(func(character rune) rune {
		if character == '\t' || character == '\n' || character == '\r' ||
			character >= 0x20 && character <= 0xd7ff ||
			character >= 0xe000 && character <= 0xfffd ||
			character >= 0x10000 && character <= 0x10ffff {
			return character
		}
		return -1
	}, value)
}

func escapeXML(value string) string {
	var result bytes.Buffer
	_ = xml.EscapeText(&result, []byte(cleanXMLText(value)))
	return result.String()
}

const xmlDeclaration = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`

const contentTypesXML = xmlDeclaration + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/><Override PartName="/word/numbering.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.numbering+xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/><Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/></Types>`

const rootRelationshipsXML = xmlDeclaration + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/></Relationships>`

const documentRelationshipsXML = xmlDeclaration + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/></Relationships>`

const stylesXML = xmlDeclaration + `<w:styles xmlns:w="` + wordNamespace + `"><w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Times New Roman" w:hAnsi="Times New Roman" w:eastAsia="宋体"/><w:color w:val="000000"/><w:sz w:val="24"/><w:szCs w:val="24"/><w:lang w:val="zh-CN" w:eastAsia="zh-CN"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:after="120" w:line="360" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults><w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style><w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="240" w:after="360"/><w:jc w:val="center"/></w:pPr><w:rPr><w:rFonts w:eastAsia="黑体"/><w:b/><w:color w:val="000000"/><w:sz w:val="40"/><w:szCs w:val="40"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:pPr><w:keepNext/><w:keepLines/><w:spacing w:before="280" w:after="160"/><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:rFonts w:eastAsia="黑体"/><w:b/><w:color w:val="000000"/><w:sz w:val="32"/><w:szCs w:val="32"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Heading1"/><w:next w:val="Normal"/><w:pPr><w:spacing w:before="240" w:after="140"/><w:outlineLvl w:val="1"/></w:pPr><w:rPr><w:sz w:val="28"/><w:szCs w:val="28"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Heading1"/><w:next w:val="Normal"/><w:pPr><w:spacing w:before="200" w:after="120"/><w:outlineLvl w:val="2"/></w:pPr><w:rPr><w:sz w:val="26"/><w:szCs w:val="26"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Heading4"><w:name w:val="heading 4"/><w:basedOn w:val="Heading1"/><w:next w:val="Normal"/><w:pPr><w:spacing w:before="180" w:after="120"/><w:outlineLvl w:val="3"/></w:pPr><w:rPr><w:sz w:val="24"/><w:szCs w:val="24"/></w:rPr></w:style></w:styles>`
