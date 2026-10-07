package documentexport

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/bidformat"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// ErrUnsupportedTenderFormat leaves the supplied content untouched when the
// native renderer cannot honour a stated requirement. Callers should explain
// the requirement rather than export a different style as a compliant bid.
var ErrUnsupportedTenderFormat = errors.New("unsupported tender document format")

// BuildBidDOCXWithSpec applies an already resolved tender specification. The
// provenance and conflict checks belong to the caller; nil fields retain the
// defaults of the selected business or technical layout. Anonymous technical
// content must be generated separately: this function is not an anonymizer.
func BuildBidDOCXWithSpec(title, markdown string, spec bidformat.Spec) ([]byte, error) {
	if err := validateBidSpec(spec); err != nil {
		return nil, err
	}
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
	w := &documentWriter{source: source, bid: &bidLayout{headings: make(map[*ast.Heading]bidHeadingEntry)}, format: &spec}
	if err := w.validateRuleTables(doc); err != nil {
		return nil, err
	}
	technical := spec.Scope == bidformat.ScopeTechnical
	coverEnabled := specBool(spec.Structure.Cover, !technical)
	tocEnabled := specBool(spec.Structure.TOC, !technical)
	headerEnabled := specBool(spec.Page.Header, !technical)
	footerEnabled := specBool(spec.Page.Footer, !technical)
	pageNumbers := specBool(spec.Page.PageNumbers, !technical && footerEnabled)
	cover := bidCover{title: title, project: "[项目名称]", number: "[项目编号]", supplier: "[投标人名称]", representative: "[签字或盖章]", date: "[日期]"}
	var planned []string
	if coverEnabled {
		cover, planned = w.bidFrontMatter(doc, title)
	} else {
		// A generated business cover must not be silently turned into the body
		// of an anonymous technical bid. The caller can select a technical draft.
		if w.anonymousFormat() && hasBidCoverIdentity(doc, w) {
			return nil, fmt.Errorf("%w: 技术暗标正文包含商务封面身份字段，请单独生成技术稿", ErrUnsupportedTenderFormat)
		}
		planned = removeBidSourceContents(doc, w)
	}
	entries := w.bidOutline(doc)
	w.body.WriteString(xmlDeclaration + `<w:document xmlns:w="` + wordNamespace + `" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body>`)
	if coverEnabled {
		w.writeBidCover(cover)
		w.specSectionBreak(false, true, headerEnabled, footerEnabled, pageNumbers)
	}
	if tocEnabled {
		w.writeBidContents(entries, planned)
		w.specSectionBreak(false, false, headerEnabled, footerEnabled, pageNumbers)
	}
	if len(entries) == 0 && !technical {
		w.paragraphRaw("投标文件正文", paragraphOptions{style: "BidFirstChapter"}, runOptions{})
	}
	w.blocks(doc, paragraphOptions{})
	if w.err != nil {
		return nil, w.err
	}
	w.body.WriteString(specSectionProperties(spec, true, false, headerEnabled, footerEnabled, pageNumbers) + `</w:body></w:document>`)
	contentTypes, relationships := contentTypesXML, documentRelationshipsXML
	addPart := func(name, contentType, relation, id string) {
		contentTypes = strings.Replace(contentTypes, `</Types>`, `<Override PartName="/word/`+name+`" ContentType="`+contentType+`"/></Types>`, 1)
		relationships = strings.Replace(relationships, `</Relationships>`, `<Relationship Id="`+id+`" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/`+relation+`" Target="`+name+`"/></Relationships>`, 1)
	}
	var extra []docxPart
	if headerEnabled {
		addPart("header1.xml", "application/vnd.openxmlformats-officedocument.wordprocessingml.header+xml", "header", "rIdBidHeader")
		extra = append(extra, docxPart{"word/header1.xml", bidHeaderXML(cover.title)})
	}
	if footerEnabled {
		addPart("footer1.xml", "application/vnd.openxmlformats-officedocument.wordprocessingml.footer+xml", "footer", "rIdBidFooter")
		footer := bidFooterXML
		if !pageNumbers {
			footer = xmlDeclaration + `<w:ftr xmlns:w="` + wordNamespace + `"><w:p><w:pPr><w:pStyle w:val="BidFooter"/></w:pPr></w:p></w:ftr>`
		}
		extra = append(extra, docxPart{"word/footer1.xml", footer})
	}
	if tocEnabled || footerEnabled && pageNumbers {
		addPart("settings.xml", "application/vnd.openxmlformats-officedocument.wordprocessingml.settings+xml", "settings", "rIdBidSettings")
		extra = append(extra, docxPart{"word/settings.xml", xmlDeclaration + `<w:settings xmlns:w="` + wordNamespace + `"><w:updateFields w:val="true"/></w:settings>`})
	}
	creator := "WeKnora"
	if w.anonymousFormat() {
		creator = ""
	}
	parts := []docxPart{
		{"[Content_Types].xml", contentTypes}, {"_rels/.rels", rootRelationshipsXML},
		{"word/document.xml", w.body.String()}, {"word/styles.xml", bidSpecStylesXML(spec)},
		{"word/numbering.xml", w.numberingXML()}, {"word/_rels/document.xml.rels", relationships},
	}
	parts = append(parts, extra...)
	parts = append(parts,
		docxPart{"docProps/core.xml", xmlDeclaration + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>` + escapeXML(title) + `</dc:title><dc:creator>` + creator + `</dc:creator></cp:coreProperties>`},
		docxPart{"docProps/app.xml", xmlDeclaration + `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Application>WeKnora</Application></Properties>`},
	)
	return writeDOCXParts(parts)
}

func specBool(value *bool, fallback bool) bool {
	if value != nil {
		return *value
	}
	return fallback
}

func validateBidSpec(spec bidformat.Spec) error {
	unsupported := func(message string) error { return fmt.Errorf("%w: %s", ErrUnsupportedTenderFormat, message) }
	if spec.Scope != "" && spec.Scope != bidformat.ScopeAll && spec.Scope != bidformat.ScopeBusiness && spec.Scope != bidformat.ScopeTechnical {
		return unsupported("unknown bid scope")
	}
	for _, rule := range []bidformat.TextStyle{spec.Body, spec.Heading, spec.Table, spec.AllText} {
		if rule.FontFamily != nil && (strings.TrimSpace(*rule.FontFamily) == "" || cleanXMLText(*rule.FontFamily) != *rule.FontFamily || utf8.RuneCountInString(*rule.FontFamily) > 128 || strings.ContainsAny(*rule.FontFamily, "\r\n\t")) {
			return unsupported("invalid font family")
		}
		if rule.FontSizeHalfPoints != nil && (*rule.FontSizeHalfPoints < 8 || *rule.FontSizeHalfPoints > 200) {
			return unsupported("font size is outside the supported range")
		}
		if rule.Color != nil && !regexp.MustCompile(`^[0-9a-fA-F]{6}$`).MatchString(*rule.Color) {
			return unsupported("invalid text colour")
		}
		if specBool(rule.Shading, false) {
			return unsupported("specified background fill needs an explicit fill colour")
		}
		if rule.CharacterSpacingTwips != nil && (*rule.CharacterSpacingTwips < -200 || *rule.CharacterSpacingTwips > 400) {
			return unsupported("character spacing is outside the supported range")
		}
		if rule.PositionHalfPoints != nil && (*rule.PositionHalfPoints < -200 || *rule.PositionHalfPoints > 200) {
			return unsupported("text position is outside the supported range")
		}
		if rule.LineRule != nil && *rule.LineRule != "auto" && *rule.LineRule != "exact" {
			return unsupported("unsupported line spacing rule")
		}
		if rule.LineSpacingTwips != nil && (*rule.LineSpacingTwips < 1 || *rule.LineSpacingTwips > 7200) {
			return unsupported("line spacing is outside the supported range")
		}
		if rule.Alignment != nil && *rule.Alignment != "left" && *rule.Alignment != "right" && *rule.Alignment != "center" && *rule.Alignment != "both" {
			return unsupported("unsupported paragraph alignment")
		}
		if rule.FirstLineChars != nil && (*rule.FirstLineChars < 0 || *rule.FirstLineChars > 1000) {
			return unsupported("first-line indentation is outside the supported range")
		}
		for _, spacing := range []*int{rule.SpaceBeforeTwips, rule.SpaceAfterTwips} {
			if spacing != nil && (*spacing < 0 || *spacing > 7200) {
				return unsupported("paragraph spacing is outside the supported range")
			}
		}
	}
	if spec.Page.Paper != nil && !strings.EqualFold(*spec.Page.Paper, "A4") {
		return unsupported("only A4 paper is supported")
	}
	for _, margin := range []*int{spec.Page.MarginTopTwips, spec.Page.MarginBottomTwips, spec.Page.MarginLeftTwips, spec.Page.MarginRightTwips} {
		if margin != nil && (*margin < 0 || *margin > pageHeight) {
			return unsupported("invalid page margin")
		}
	}
	if specPrintableWidth(spec) < 1440 || pageHeight-specMargin(spec.Page.MarginTopTwips, bidOtherMargin)-specMargin(spec.Page.MarginBottomTwips, bidOtherMargin) < 1440 {
		return unsupported("page margins leave insufficient writing space")
	}
	if specBool(spec.Structure.BackCover, false) {
		return unsupported("specified back cover requires its supplied tender template")
	}
	if specBool(spec.Structure.BlankPages, false) {
		return unsupported("required blank-page placement must be supplied explicitly")
	}
	if !specBool(spec.Page.Footer, spec.Scope != bidformat.ScopeTechnical) && specBool(spec.Page.PageNumbers, false) {
		return unsupported("page numbering without a footer requires a specified alternative placement")
	}
	return nil
}

func specMargin(value *int, fallback int) int {
	if value != nil {
		return *value
	}
	return fallback
}

func specPrintableWidth(spec bidformat.Spec) int {
	return pageWidth - specMargin(spec.Page.MarginLeftTwips, bidLeftMargin) - specMargin(spec.Page.MarginRightTwips, bidOtherMargin)
}

func (w *documentWriter) anonymousFormat() bool {
	return w.format != nil && specBool(w.format.Anonymous, w.format.Scope == bidformat.ScopeTechnical)
}

// A more specific target can express a tender's explicit exception to a
// document-wide requirement. The resolver rejects contradictory evidence.
func mergeRule(base, target bidformat.TextStyle) bidformat.TextStyle {
	if target.FontFamily != nil {
		base.FontFamily = target.FontFamily
	}
	if target.FontSizeHalfPoints != nil {
		base.FontSizeHalfPoints = target.FontSizeHalfPoints
	}
	if target.Color != nil {
		base.Color = target.Color
	}
	if target.Shading != nil {
		base.Shading = target.Shading
	}
	if target.CharacterSpacingTwips != nil {
		base.CharacterSpacingTwips = target.CharacterSpacingTwips
	}
	if target.PositionHalfPoints != nil {
		base.PositionHalfPoints = target.PositionHalfPoints
	}
	if target.Bold != nil {
		base.Bold = target.Bold
	}
	if target.Italic != nil {
		base.Italic = target.Italic
	}
	if target.Underline != nil {
		base.Underline = target.Underline
	}
	if target.LineSpacingTwips != nil {
		base.LineSpacingTwips = target.LineSpacingTwips
	}
	if target.LineRule != nil {
		base.LineRule = target.LineRule
	}
	if target.Alignment != nil {
		base.Alignment = target.Alignment
	}
	if target.FirstLineChars != nil {
		base.FirstLineChars = target.FirstLineChars
	}
	if target.SpaceBeforeTwips != nil {
		base.SpaceBeforeTwips = target.SpaceBeforeTwips
	}
	if target.SpaceAfterTwips != nil {
		base.SpaceAfterTwips = target.SpaceAfterTwips
	}
	return base
}

func (w *documentWriter) paragraphRule(style string) bidformat.TextStyle {
	if w.format == nil {
		return bidformat.TextStyle{}
	}
	return specStyle(*w.format, style)
}

func specStyle(spec bidformat.Spec, style string) bidformat.TextStyle {
	rule := spec.AllText
	switch {
	case style == "BidTable":
		rule = mergeRule(rule, spec.Table)
	case strings.HasPrefix(style, "Heading") || style == "BidFirstChapter":
		rule = mergeRule(rule, spec.Heading)
	case style == "" || style == "Normal" || strings.HasPrefix(style, "BidBody") || style == "BidForm" || style == "BidList":
		rule = mergeRule(rule, spec.Body)
	}
	return rule
}

func ruleRunProperties(rule bidformat.TextStyle) string {
	var result strings.Builder
	if rule.FontFamily != nil {
		font := escapeXML(*rule.FontFamily)
		result.WriteString(`<w:rFonts w:ascii="` + font + `" w:hAnsi="` + font + `" w:eastAsia="` + font + `" w:cs="` + font + `"/>`)
	}
	if rule.Color != nil {
		result.WriteString(`<w:color w:val="` + escapeXML(*rule.Color) + `"/>`)
	}
	if rule.Shading != nil && !*rule.Shading {
		result.WriteString(`<w:shd w:val="nil"/>`)
	}
	if rule.CharacterSpacingTwips != nil {
		fmt.Fprintf(&result, `<w:spacing w:val="%d"/>`, *rule.CharacterSpacingTwips)
	}
	if rule.PositionHalfPoints != nil {
		fmt.Fprintf(&result, `<w:position w:val="%d"/>`, *rule.PositionHalfPoints)
	}
	if rule.Bold != nil {
		value := "0"
		if *rule.Bold {
			value = "1"
		}
		result.WriteString(`<w:b w:val="` + value + `"/><w:bCs w:val="` + value + `"/>`)
	}
	if rule.Italic != nil {
		value := "0"
		if *rule.Italic {
			value = "1"
		}
		result.WriteString(`<w:i w:val="` + value + `"/><w:iCs w:val="` + value + `"/>`)
	}
	if rule.Underline != nil {
		value := "none"
		if *rule.Underline {
			value = "single"
		}
		result.WriteString(`<w:u w:val="` + value + `"/>`)
	}
	if rule.FontSizeHalfPoints != nil {
		fmt.Fprintf(&result, `<w:sz w:val="%d"/><w:szCs w:val="%d"/>`, *rule.FontSizeHalfPoints, *rule.FontSizeHalfPoints)
	}
	return result.String()
}

func ruleParagraphProperties(rule bidformat.TextStyle) string {
	var result strings.Builder
	if rule.LineSpacingTwips != nil || rule.LineRule != nil || rule.SpaceBeforeTwips != nil || rule.SpaceAfterTwips != nil {
		result.WriteString(`<w:spacing`)
		if rule.LineSpacingTwips != nil {
			fmt.Fprintf(&result, ` w:line="%d"`, *rule.LineSpacingTwips)
		}
		if rule.LineRule != nil {
			result.WriteString(` w:lineRule="` + escapeXML(*rule.LineRule) + `"`)
		}
		if rule.SpaceBeforeTwips != nil {
			fmt.Fprintf(&result, ` w:before="%d"`, *rule.SpaceBeforeTwips)
		}
		if rule.SpaceAfterTwips != nil {
			fmt.Fprintf(&result, ` w:after="%d"`, *rule.SpaceAfterTwips)
		}
		result.WriteString(`/>`)
	}
	if rule.Alignment != nil {
		result.WriteString(`<w:jc w:val="` + escapeXML(*rule.Alignment) + `"/>`)
	}
	if rule.FirstLineChars != nil {
		fontSize := 24
		if rule.FontSizeHalfPoints != nil {
			fontSize = *rule.FontSizeHalfPoints
		}
		fmt.Fprintf(&result, `<w:ind w:left="0" w:right="0" w:firstLineChars="%d" w:firstLine="%d"/>`, *rule.FirstLineChars, fontSize*10*(*rule.FirstLineChars)/100)
	}
	return result.String()
}

func specSectionProperties(spec bidformat.Spec, body, cover, header, footer, pageNumbers bool) string {
	var result strings.Builder
	result.WriteString(`<w:sectPr>`)
	if body && header {
		result.WriteString(`<w:headerReference w:type="default" r:id="rIdBidHeader"/>`)
	}
	if body && footer {
		result.WriteString(`<w:footerReference w:type="default" r:id="rIdBidFooter"/>`)
	}
	result.WriteString(`<w:type w:val="nextPage"/>`)
	fmt.Fprintf(&result, `<w:pgSz w:w="%d" w:h="%d"/><w:pgMar w:top="%d" w:right="%d" w:bottom="%d" w:left="%d" w:header="708" w:footer="708" w:gutter="0"/>`, pageWidth, pageHeight, specMargin(spec.Page.MarginTopTwips, bidOtherMargin), specMargin(spec.Page.MarginRightTwips, bidOtherMargin), specMargin(spec.Page.MarginBottomTwips, bidOtherMargin), specMargin(spec.Page.MarginLeftTwips, bidLeftMargin))
	if body && footer && pageNumbers {
		result.WriteString(`<w:pgNumType w:fmt="decimal" w:start="1"/>`)
	}
	if cover {
		result.WriteString(`<w:titlePg/>`)
	}
	result.WriteString(`</w:sectPr>`)
	return result.String()
}

func (w *documentWriter) specSectionBreak(body, cover, header, footer, pageNumbers bool) {
	w.body.WriteString(`<w:p><w:pPr><w:spacing w:before="0" w:after="0" w:line="20" w:lineRule="exact"/>` + specSectionProperties(*w.format, body, cover, header, footer, pageNumbers) + `</w:pPr></w:p>`)
}

func hasBidCoverIdentity(doc ast.Node, w *documentWriter) bool {
	for node := doc.FirstChild(); node != nil; node = node.NextSibling() {
		if _, heading := node.(*ast.Heading); heading {
			continue
		}
		if _, paragraph := node.(*ast.Paragraph); !paragraph {
			break
		}
		for _, line := range bidSourceLines(node, w.source) {
			if regexp.MustCompile(`^(投标人(?:名称)?|供应商(?:名称)?|法定代表人|授权代表|被授权人|单位名称|公司名称)[：:(（]`).MatchString(compactBidText(line)) {
				return true
			}
		}
	}
	return false
}

func removeBidSourceContents(doc ast.Node, w *documentWriter) []string {
	var planned []string
	for node := doc.FirstChild(); node != nil; node = node.NextSibling() {
		heading, ok := node.(*ast.Heading)
		if !ok || compactBidText(w.inlineText(heading)) != "目录" {
			continue
		}
		next := node.NextSibling()
		doc.RemoveChild(doc, node)
		for next != nil {
			if _, isHeading := next.(*ast.Heading); isHeading {
				break
			}
			current := next
			next = next.NextSibling()
			planned = append(planned, bidSourceLines(current, w.source)...)
			doc.RemoveChild(doc, current)
		}
		break
	}
	return planned
}

func minimumRuleCellWidth(rule bidformat.TextStyle) int {
	if rule.FontSizeHalfPoints == nil {
		return 1
	}
	minimum := *rule.FontSizeHalfPoints*10*2 + 160 // Two full-width characters plus cell padding.
	if rule.FirstLineChars != nil {
		minimum += *rule.FontSizeHalfPoints * 10 * (*rule.FirstLineChars) / 100
	}
	return minimum
}

func columnWidthsWithMinimum(weights []int, total, minimum int) []int {
	remaining := max(total-minimum*len(weights), 0)
	widths := columnWidths(weights, remaining)
	for i := range widths {
		widths[i] += minimum
	}
	return widths
}

func (w *documentWriter) validateRuleTables(doc ast.Node) error {
	rule := w.paragraphRule("BidTable")
	if rule.FontSizeHalfPoints == nil {
		return nil
	}
	return ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if table, ok := node.(*extast.Table); ok && len(table.Alignments)*minimumRuleCellWidth(rule) > specPrintableWidth(*w.format) {
			return ast.WalkStop, fmt.Errorf("%w: %d 列表格无法在指定页边距及 %.1f 磅字号下保持可读宽度，请按招标要求拆表或提供横向页面模板", ErrUnsupportedTenderFormat, len(table.Alignments), float64(*rule.FontSizeHalfPoints)/2)
		}
		return ast.WalkContinue, nil
	})
}

// The input is a trusted package-owned stylesheet. Replace only native Word
// properties present in the resolved specification, retaining unrelated layout.
func bidSpecStylesXML(spec bidformat.Spec) string {
	styleRE := regexp.MustCompile(`(?s)<w:style\b[^>]*w:styleId="([^"]+)"[^>]*>.*?</w:style>`)
	styles := styleRE.ReplaceAllStringFunc(bidStylesXML, func(style string) string {
		id := styleRE.FindStringSubmatch(style)[1]
		rule := specStyle(spec, id)
		style = overrideStylePropertyContainer(style, "rPr", ruleRunProperties(rule), runRulePropertyNames(rule))
		style = overrideStylePropertyContainer(style, "pPr", ruleParagraphProperties(rule), paragraphRulePropertyNames(rule))
		if spec.Scope == bidformat.ScopeTechnical && (strings.HasPrefix(id, "Heading") || id == "BidFirstChapter") {
			style = overrideStylePropertyContainer(style, "pPr", `<w:pageBreakBefore w:val="0"/>`, []string{"pageBreakBefore"})
		}
		return style
	})
	rule := mergeRule(spec.AllText, spec.Body)
	defaultsRE := regexp.MustCompile(`(?s)<w:docDefaults>.*?</w:docDefaults>`)
	styles = defaultsRE.ReplaceAllStringFunc(styles, func(defaults string) string {
		defaults = overrideStylePropertyContainer(defaults, "rPr", ruleRunProperties(rule), runRulePropertyNames(rule))
		return overrideStylePropertyContainer(defaults, "pPr", ruleParagraphProperties(rule), paragraphRulePropertyNames(rule))
	})
	return styles
}

func runRulePropertyNames(rule bidformat.TextStyle) []string {
	var names []string
	if rule.FontFamily != nil {
		names = append(names, "rFonts")
	}
	if rule.FontSizeHalfPoints != nil {
		names = append(names, "sz", "szCs")
	}
	if rule.Color != nil {
		names = append(names, "color")
	}
	if rule.Shading != nil {
		names = append(names, "shd")
	}
	if rule.CharacterSpacingTwips != nil {
		names = append(names, "spacing")
	}
	if rule.PositionHalfPoints != nil {
		names = append(names, "position")
	}
	if rule.Bold != nil {
		names = append(names, "b", "bCs")
	}
	if rule.Italic != nil {
		names = append(names, "i", "iCs")
	}
	if rule.Underline != nil {
		names = append(names, "u")
	}
	return names
}

func paragraphRulePropertyNames(rule bidformat.TextStyle) []string {
	var names []string
	if rule.LineSpacingTwips != nil || rule.LineRule != nil || rule.SpaceBeforeTwips != nil || rule.SpaceAfterTwips != nil {
		names = append(names, "spacing")
	}
	if rule.Alignment != nil {
		names = append(names, "jc")
	}
	if rule.FirstLineChars != nil {
		names = append(names, "ind")
	}
	return names
}

func overrideStylePropertyContainer(style, container, replacement string, names []string) string {
	if len(names) == 0 {
		return style
	}
	re := regexp.MustCompile(`(?s)<w:` + container + `>.*?</w:` + container + `>`)
	if !re.MatchString(style) {
		if container == "pPr" && strings.Contains(style, `<w:rPr>`) {
			return strings.Replace(style, `<w:rPr>`, `<w:pPr>`+replacement+`</w:pPr><w:rPr>`, 1)
		}
		return strings.Replace(style, `</w:style>`, `<w:`+container+`>`+replacement+`</w:`+container+`></w:style>`, 1)
	}
	return re.ReplaceAllStringFunc(style, func(properties string) string {
		currentReplacement := replacement
		for _, name := range names {
			propertyRE := regexp.MustCompile(`<w:` + name + `(?:\s[^>]*)?/>`)
			if name == "spacing" {
				oldProperty := propertyRE.FindString(properties)
				newProperty := propertyRE.FindString(currentReplacement)
				if oldProperty != "" && newProperty != "" {
					currentReplacement = strings.Replace(currentReplacement, newProperty, mergePropertyAttributes("spacing", oldProperty, newProperty), 1)
				}
			}
			properties = propertyRE.ReplaceAllString(properties, "")
		}
		return strings.Replace(properties, `</w:`+container+`>`, currentReplacement+`</w:`+container+`>`, 1)
	})
}

func mergePropertyAttributes(name, oldProperty, newProperty string) string {
	attributesRE := regexp.MustCompile(`\s(w:[A-Za-z]+)="([^"]*)"`)
	var names []string
	values := map[string]string{}
	for _, property := range []string{oldProperty, newProperty} {
		for _, attribute := range attributesRE.FindAllStringSubmatch(property, -1) {
			if _, found := values[attribute[1]]; !found {
				names = append(names, attribute[1])
			}
			values[attribute[1]] = attribute[2]
		}
	}
	var result strings.Builder
	result.WriteString(`<w:` + name)
	for _, attribute := range names {
		result.WriteString(` ` + attribute + `="` + values[attribute] + `"`)
	}
	result.WriteString(`/>`)
	return result.String()
}
