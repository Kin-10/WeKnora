package documentexport

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

const (
	// A4, 30 mm binding margin and 25 mm outer/top/bottom margins.
	bidLeftMargin     = 1701
	bidOtherMargin    = 1417
	bidPrintableWidth = pageWidth - bidLeftMargin - bidOtherMargin
)

type bidHeadingEntry struct {
	node            *ast.Heading
	label           string
	level, bookmark int
}

type bidLayout struct {
	headings    map[*ast.Heading]bidHeadingEntry
	chapterSeen bool
}

type bidCover struct {
	title, project, number, supplier, representative, date, version string
}

// BuildBidDOCX lays out a business/open bid as a bound document: a separate
// cover, a native updateable TOC and numbered body sections. It formats only
// supplied text; it does not fill in company facts, prices or missing chapters.
// Anonymous technical bids have project-specific rules and use a separate
// template rather than this business-bid layout.
func BuildBidDOCX(title, markdown string) ([]byte, error) {
	markdown = cleanXMLText(markdown)
	if strings.TrimSpace(markdown) == "" {
		return nil, ErrEmptyContent
	}
	title = strings.TrimSpace(cleanXMLText(title))
	if title == "" || title == "文档" {
		title = "投标文件"
	}
	source := []byte(markdown)
	doc := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader(source))
	w := &documentWriter{source: source, bid: &bidLayout{headings: make(map[*ast.Heading]bidHeadingEntry)}}
	cover, planned := w.bidFrontMatter(doc, title)
	entries := w.bidOutline(doc)
	w.body.WriteString(xmlDeclaration + `<w:document xmlns:w="` + wordNamespace + `" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body>`)
	w.writeBidCover(cover)
	w.bidSectionBreak(false, true)
	w.writeBidContents(entries, planned)
	w.bidSectionBreak(false, false)
	if len(entries) == 0 {
		w.paragraphRaw("投标文件正文", paragraphOptions{style: "BidFirstChapter"}, runOptions{})
	}
	w.blocks(doc, paragraphOptions{})
	w.body.WriteString(bidSectionProperties(true, false) + `</w:body></w:document>`)

	contentTypes := strings.Replace(contentTypesXML, `</Types>`, `<Override PartName="/word/header1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.header+xml"/><Override PartName="/word/footer1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.footer+xml"/><Override PartName="/word/settings.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.settings+xml"/></Types>`, 1)
	relationships := strings.Replace(documentRelationshipsXML, `</Relationships>`, `<Relationship Id="rIdBidHeader" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header1.xml"/><Relationship Id="rIdBidFooter" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/footer" Target="footer1.xml"/><Relationship Id="rIdBidSettings" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/settings" Target="settings.xml"/></Relationships>`, 1)
	return writeDOCXParts([]docxPart{
		{"[Content_Types].xml", contentTypes}, {"_rels/.rels", rootRelationshipsXML},
		{"word/document.xml", w.body.String()}, {"word/styles.xml", bidStylesXML},
		{"word/numbering.xml", w.numberingXML()}, {"word/_rels/document.xml.rels", relationships},
		{"word/header1.xml", bidHeaderXML(cover.title)}, {"word/footer1.xml", bidFooterXML},
		{"word/settings.xml", xmlDeclaration + `<w:settings xmlns:w="` + wordNamespace + `"><w:updateFields w:val="true"/></w:settings>`},
		{"docProps/core.xml", xmlDeclaration + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>` + escapeXML(title) + `</dc:title><dc:creator>WeKnora</dc:creator><dc:description>商务标书版式 v1</dc:description></cp:coreProperties>`},
		{"docProps/app.xml", xmlDeclaration + `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Application>WeKnora</Application></Properties>`},
	})
}

// Move only a recognized cover and table of contents out of the body. Any
// paragraph containing additional prose, an attachment, or an unknown field is
// retained verbatim. Missing cover fields remain visibly editable placeholders.
func (w *documentWriter) bidFrontMatter(doc ast.Node, title string) (bidCover, []string) {
	cover := bidCover{title: title, project: "[项目名称]", number: "[项目编号]", supplier: "[投标人名称]", representative: "[签字或盖章]", date: "[日期]"}
	promotedTitle := false
	if first, ok := doc.FirstChild().(*ast.Heading); ok && first.Level == 1 &&
		strings.TrimSpace(w.inlineText(first)) == title && isPlainHeading(first) {
		doc.RemoveChild(doc, first)
		promotedTitle = true
	}
	var planned []string
	front, inContents := true, false
	for node := doc.FirstChild(); node != nil; {
		next := node.NextSibling()
		if heading, ok := node.(*ast.Heading); ok {
			if !promotedTitle && heading.Level == 1 && strings.TrimSpace(w.inlineText(heading)) == title && isPlainHeading(heading) {
				doc.RemoveChild(doc, node)
				promotedTitle, front = true, true
				node = next
				continue
			}
			if compactBidText(w.inlineText(heading)) == "目录" {
				inContents = true
				doc.RemoveChild(doc, node)
				node = next
				continue
			}
			break
		}
		if inContents {
			if _, separator := node.(*ast.ThematicBreak); !separator {
				planned = append(planned, bidSourceLines(node, w.source)...)
			}
			doc.RemoveChild(doc, node)
		} else if _, separator := node.(*ast.ThematicBreak); separator {
			doc.RemoveChild(doc, node)
		} else if _, paragraph := node.(*ast.Paragraph); paragraph && front {
			lines := bidSourceLines(node, w.source)
			allFields := len(lines) > 0
			for _, line := range lines {
				if !cover.readField(line) {
					allFields = false
				}
			}
			if allFields {
				doc.RemoveChild(doc, node)
			} else {
				front = false
			}
		} else {
			front = false
		}
		node = next
	}
	return cover, planned
}

func bidSourceLines(node ast.Node, source []byte) []string {
	var lines []string
	_ = ast.Walk(node, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || (child.Kind() != ast.KindParagraph && child.Kind() != ast.KindTextBlock) {
			return ast.WalkContinue, nil
		}
		for i := 0; i < child.Lines().Len(); i++ {
			line := child.Lines().At(i)
			value := strings.TrimSpace(strings.ReplaceAll(string(line.Value(source)), "**", ""))
			if value != "" {
				lines = append(lines, value)
			}
		}
		return ast.WalkSkipChildren, nil
	})
	return lines
}

func compactBidText(value string) string {
	return strings.Join(strings.Fields(strings.Trim(value, "* \t")), "")
}

func (cover *bidCover) readField(line string) bool {
	line = strings.TrimSpace(line)
	if line == "正本" || line == "副本" {
		cover.version = line
		return true
	}
	label, value, found := strings.Cut(line, "：")
	if !found {
		label, value, found = strings.Cut(line, ":")
	}
	if !found {
		return false
	}
	label = compactBidText(label)
	value = strings.TrimSpace(value)
	var field *string
	switch {
	case label == "项目名称" || label == "项目名称及标包" || label == "采购项目名称":
		field = &cover.project
	case label == "项目编号" || label == "招标编号" || label == "采购编号":
		field = &cover.number
	case strings.HasPrefix(label, "供应商名称") || strings.HasPrefix(label, "投标人名称") || label == "投标人" || label == "投标单位":
		field = &cover.supplier
	case strings.HasPrefix(label, "法定代表人") || strings.HasPrefix(label, "授权代表"):
		field = &cover.representative
	case label == "日期" || label == "投标日期":
		field = &cover.date
	default:
		return false
	}
	if value != "" {
		*field = value
	}
	return true
}

func (w *documentWriter) writeBidCover(cover bidCover) {
	if cover.version != "" {
		w.paragraphRaw(cover.version, paragraphOptions{style: "CoverVersion"}, runOptions{})
	}
	w.paragraphRaw(cover.project, paragraphOptions{style: "CoverProject"}, runOptions{})
	w.paragraphRaw("项目编号："+cover.number, paragraphOptions{style: "CoverNumber"}, runOptions{})
	w.paragraphRaw(cover.title, paragraphOptions{style: "Title"}, runOptions{})
	w.paragraphRaw("投标人（盖章）："+cover.supplier, paragraphOptions{style: "CoverField"}, runOptions{})
	w.paragraphRaw("法定代表人或授权代表："+cover.representative, paragraphOptions{style: "CoverField"}, runOptions{})
	w.paragraphRaw("日期："+cover.date, paragraphOptions{style: "CoverField"}, runOptions{})
}

func bidSectionProperties(body, cover bool) string {
	var properties strings.Builder
	properties.WriteString(`<w:sectPr>`)
	if body {
		properties.WriteString(`<w:headerReference w:type="default" r:id="rIdBidHeader"/><w:footerReference w:type="default" r:id="rIdBidFooter"/>`)
	}
	properties.WriteString(`<w:type w:val="nextPage"/>`)
	fmt.Fprintf(&properties, `<w:pgSz w:w="%d" w:h="%d"/><w:pgMar w:top="%d" w:right="%d" w:bottom="%d" w:left="%d" w:header="708" w:footer="708" w:gutter="0"/>`, pageWidth, pageHeight, bidOtherMargin, bidOtherMargin, bidOtherMargin, bidLeftMargin)
	if body {
		properties.WriteString(`<w:pgNumType w:fmt="decimal" w:start="1"/>`)
	}
	if cover {
		properties.WriteString(`<w:titlePg/>`)
	}
	properties.WriteString(`</w:sectPr>`)
	return properties.String()
}

func (w *documentWriter) bidSectionBreak(body, cover bool) {
	w.body.WriteString(`<w:p><w:pPr><w:spacing w:before="0" w:after="0" w:line="20" w:lineRule="exact"/>` + bidSectionProperties(body, cover) + `</w:pPr></w:p>`)
}

func (w *documentWriter) bidOutline(doc ast.Node) []bidHeadingEntry {
	minLevel := 6
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if heading, ok := node.(*ast.Heading); ok && entering {
			minLevel = min(minLevel, heading.Level)
		}
		return ast.WalkContinue, nil
	})
	var entries []bidHeadingEntry
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if heading, ok := node.(*ast.Heading); ok && entering {
			entry := bidHeadingEntry{node: heading, label: strings.TrimSpace(w.inlineText(heading)), level: min(4, heading.Level-minLevel+1), bookmark: len(entries) + 1}
			if entry.level == 1 && isBidContinuationHeading(entry.label) {
				entry.level = 2
			}
			w.bid.headings[heading] = entry
			entries = append(entries, entry)
		}
		return ast.WalkContinue, nil
	})
	return entries
}

func isBidContinuationHeading(label string) bool {
	return strings.HasSuffix(label, "（续）") || strings.HasSuffix(label, "(续)") || strings.HasSuffix(label, "续写")
}

func (w *documentWriter) bidHeading(heading *ast.Heading, options paragraphOptions) {
	entry := w.bid.headings[heading]
	options.style = fmt.Sprintf("Heading%d", entry.level)
	options.bookmark = entry.bookmark
	if entry.level == 1 {
		if !w.bid.chapterSeen {
			options.style = "BidFirstChapter"
		}
		w.bid.chapterSeen = true
	}
	w.paragraph(heading, options)
}

func (w *documentWriter) writeBidContents(entries []bidHeadingEntry, planned []string) {
	w.paragraphRaw("目 录", paragraphOptions{style: "TOCHeading"}, runOptions{})
	// A native complex TOC field replaces this cached result on update. Cached
	// titles are readable in the browser, which cannot calculate Word pages.
	// No fabricated page numbers are inserted into the initial cache.
	w.body.WriteString(`<w:p><w:pPr><w:pStyle w:val="TOC1"/></w:pPr><w:r><w:fldChar w:fldCharType="begin" w:dirty="true"/></w:r><w:r><w:instrText xml:space="preserve"> TOC \o "1-3" \h \z \u </w:instrText></w:r><w:r><w:fldChar w:fldCharType="separate"/></w:r></w:p>`)
	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.level > 3 || entry.label == "" {
			continue
		}
		seen[compactBidText(entry.label)] = true
		w.paragraphStart(paragraphOptions{style: fmt.Sprintf("TOC%d", entry.level)})
		fmt.Fprintf(&w.body, `<w:hyperlink w:anchor="_BidHeading%d" w:history="1">`, entry.bookmark)
		w.run(entry.label, runOptions{})
		w.body.WriteString(`</w:hyperlink></w:p>`)
	}
	// Keep not-yet-generated entries from the answer's original contents as
	// unnumbered cached titles. Word will rebuild the TOC from actual headings
	// after the user finishes the draft; we never create empty bid chapters.
	for _, label := range planned {
		if label == "" || seen[compactBidText(label)] {
			continue
		}
		seen[compactBidText(label)] = true
		w.paragraphRaw(label, paragraphOptions{style: "TOC1"}, runOptions{})
	}
	if len(entries) == 0 && len(planned) == 0 {
		w.paragraphRaw("投标文件正文", paragraphOptions{style: "TOC1"}, runOptions{})
	}
	w.body.WriteString(`<w:p><w:pPr><w:spacing w:before="0" w:after="0" w:line="20" w:lineRule="exact"/></w:pPr><w:r><w:fldChar w:fldCharType="end"/></w:r></w:p>`)
}

var bidFormPrefix = regexp.MustCompile(`^(致[：:]|项目名称|项目编号|供应商名称|供应商[（(]|投标人|单位[：:]|日期[：:]|日[　 ]*期[：:]|法定代表人|被授权人|授权代表|地址[：:]|电话[：:]|传真[：:]|E-mail[：:]|公司名称|成立时间|注册资本|公司地址|联系电话|邮政编码|电子邮箱|投标总价)`)

func isBidFormParagraph(value string) bool {
	return bidFormPrefix.MatchString(strings.TrimSpace(value))
}

func hasBidManualLineBreak(node ast.Node, source []byte) bool {
	found := false
	_ = ast.Walk(node, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if text, ok := child.(*ast.Text); ok && entering && (text.SoftLineBreak() || text.HardLineBreak()) {
			found = true
			return ast.WalkStop, nil
		}
		if raw, ok := child.(*ast.RawHTML); ok && entering {
			markup := strings.TrimSpace(string(raw.Text(source)))
			if strings.EqualFold(markup, "<br>") || strings.EqualFold(markup, "<br/>") || strings.EqualFold(markup, "<br />") {
				found = true
				return ast.WalkStop, nil
			}
		}
		return ast.WalkContinue, nil
	})
	return found
}

func (w *documentWriter) bidCompactTableColumn(table *extast.Table, column int) bool {
	row := table.FirstChild()
	if row == nil {
		return false
	}
	cell := row.FirstChild()
	for i := 0; cell != nil && i < column; i++ {
		cell = cell.NextSibling()
	}
	if cell == nil {
		return false
	}
	label := compactBidText(w.inlineText(cell))
	return label == "序号" || label == "数量" || label == "单位" || label == "单价" || label == "合计" || label == "金额" || label == "小计" || label == "响应" || label == "偏离" || label == "是否满足"
}

func bidHeaderXML(title string) string {
	return xmlDeclaration + `<w:hdr xmlns:w="` + wordNamespace + `"><w:p><w:pPr><w:pStyle w:val="BidHeader"/><w:pBdr><w:bottom w:val="single" w:sz="4" w:space="4" w:color="808080"/></w:pBdr></w:pPr><w:r><w:t>` + escapeXML(title) + `</w:t></w:r></w:p></w:hdr>`
}

const bidFooterXML = xmlDeclaration + `<w:ftr xmlns:w="` + wordNamespace + `"><w:p><w:pPr><w:pStyle w:val="BidFooter"/></w:pPr><w:r><w:fldChar w:fldCharType="begin" w:dirty="true"/></w:r><w:r><w:instrText xml:space="preserve"> PAGE </w:instrText></w:r><w:r><w:fldChar w:fldCharType="separate"/></w:r><w:r><w:t xml:space="preserve"> </w:t></w:r><w:r><w:fldChar w:fldCharType="end"/></w:r></w:p></w:ftr>`

const bidStylesXML = xmlDeclaration + `<w:styles xmlns:w="` + wordNamespace + `">
<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Times New Roman" w:hAnsi="Times New Roman" w:eastAsia="宋体"/><w:color w:val="000000"/><w:sz w:val="24"/><w:szCs w:val="24"/><w:lang w:val="zh-CN" w:eastAsia="zh-CN"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:before="0" w:after="120" w:line="360" w:lineRule="auto"/><w:widowControl/></w:pPr></w:pPrDefault></w:docDefaults>
<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>
<w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:jc w:val="center"/><w:spacing w:before="480" w:after="1800" w:line="360" w:lineRule="auto"/></w:pPr><w:rPr><w:rFonts w:eastAsia="黑体"/><w:b/><w:color w:val="000000"/><w:sz w:val="60"/><w:szCs w:val="60"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="CoverProject"><w:name w:val="Cover project"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:jc w:val="center"/><w:spacing w:before="1600" w:after="240" w:line="360" w:lineRule="auto"/></w:pPr><w:rPr><w:b/><w:sz w:val="36"/><w:szCs w:val="36"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="CoverNumber"><w:name w:val="Cover project number"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:jc w:val="center"/><w:spacing w:before="80" w:after="600"/></w:pPr><w:rPr><w:sz w:val="28"/><w:szCs w:val="28"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="CoverField"><w:name w:val="Cover field"/><w:basedOn w:val="Normal"/><w:pPr><w:keepLines/><w:spacing w:before="240" w:after="160" w:line="360" w:lineRule="auto"/></w:pPr><w:rPr><w:sz w:val="28"/><w:szCs w:val="28"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="CoverVersion"><w:name w:val="Cover version"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:jc w:val="right"/></w:pPr><w:rPr><w:sz w:val="28"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="BidBody"><w:name w:val="Bid body"/><w:basedOn w:val="Normal"/><w:pPr><w:jc w:val="both"/><w:ind w:firstLineChars="200" w:firstLine="480"/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="BidBodyLines"><w:name w:val="Bid body with manual lines"/><w:basedOn w:val="BidBody"/><w:pPr><w:jc w:val="left"/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="BidForm"><w:name w:val="Bid form"/><w:basedOn w:val="Normal"/><w:pPr><w:jc w:val="left"/><w:ind w:firstLine="0"/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="BidList"><w:name w:val="Bid list"/><w:basedOn w:val="Normal"/><w:pPr><w:spacing w:after="80"/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:next w:val="BidBody"/><w:pPr><w:keepNext/><w:keepLines/><w:pageBreakBefore/><w:spacing w:before="160" w:after="240"/><w:outlineLvl w:val="0"/><w:ind w:firstLine="0"/></w:pPr><w:rPr><w:rFonts w:eastAsia="黑体"/><w:b/><w:color w:val="000000"/><w:sz w:val="32"/><w:szCs w:val="32"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="BidFirstChapter"><w:name w:val="First bid chapter"/><w:basedOn w:val="Heading1"/><w:next w:val="BidBody"/><w:pPr><w:pageBreakBefore w:val="0"/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:next w:val="BidBody"/><w:pPr><w:keepNext/><w:keepLines/><w:pageBreakBefore w:val="0"/><w:spacing w:before="200" w:after="140"/><w:outlineLvl w:val="1"/></w:pPr><w:rPr><w:rFonts w:eastAsia="黑体"/><w:b/><w:color w:val="000000"/><w:sz w:val="28"/><w:szCs w:val="28"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Normal"/><w:next w:val="BidBody"/><w:pPr><w:keepNext/><w:keepLines/><w:pageBreakBefore w:val="0"/><w:spacing w:before="180" w:after="120"/><w:outlineLvl w:val="2"/></w:pPr><w:rPr><w:rFonts w:eastAsia="黑体"/><w:b/><w:color w:val="000000"/><w:sz w:val="26"/><w:szCs w:val="26"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading4"><w:name w:val="heading 4"/><w:basedOn w:val="Normal"/><w:next w:val="BidBody"/><w:pPr><w:keepNext/><w:keepLines/><w:pageBreakBefore w:val="0"/><w:spacing w:before="160" w:after="100"/><w:outlineLvl w:val="3"/></w:pPr><w:rPr><w:b/><w:color w:val="000000"/><w:sz w:val="24"/><w:szCs w:val="24"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="TOCHeading"><w:name w:val="TOC Heading"/><w:basedOn w:val="Normal"/><w:pPr><w:keepNext/><w:jc w:val="center"/><w:spacing w:before="240" w:after="400"/></w:pPr><w:rPr><w:rFonts w:eastAsia="黑体"/><w:b/><w:color w:val="000000"/><w:sz w:val="36"/><w:szCs w:val="36"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="TOC1"><w:name w:val="toc 1"/><w:basedOn w:val="Normal"/><w:pPr><w:spacing w:before="0" w:after="120" w:line="300" w:lineRule="auto"/><w:tabs><w:tab w:val="right" w:leader="dot" w:pos="` + fmtBidTOCWidth + `"/></w:tabs></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="TOC2"><w:name w:val="toc 2"/><w:basedOn w:val="TOC1"/><w:pPr><w:ind w:left="360"/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="TOC3"><w:name w:val="toc 3"/><w:basedOn w:val="TOC1"/><w:pPr><w:ind w:left="720"/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="BidTable"><w:name w:val="Bid table text"/><w:basedOn w:val="Normal"/><w:pPr><w:ind w:firstLine="0"/><w:spacing w:before="0" w:after="80" w:line="260" w:lineRule="auto"/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="BidHeader"><w:name w:val="Bid header"/><w:basedOn w:val="Normal"/><w:pPr><w:jc w:val="right"/><w:spacing w:before="0" w:after="0" w:line="240" w:lineRule="auto"/></w:pPr><w:rPr><w:color w:val="000000"/><w:sz w:val="20"/><w:szCs w:val="20"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="BidFooter"><w:name w:val="Bid footer"/><w:basedOn w:val="Normal"/><w:pPr><w:jc w:val="center"/><w:spacing w:before="0" w:after="0" w:line="240" w:lineRule="auto"/></w:pPr><w:rPr><w:color w:val="000000"/><w:sz w:val="20"/><w:szCs w:val="20"/></w:rPr></w:style>
</w:styles>`

const fmtBidTOCWidth = "8788"
