package bidformat

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

var (
	physicalPageRE    = regexp.MustCompile(`---PHYSICAL PAGE\s+(\d+)---`)
	printedPageRE     = regexp.MustCompile(`(?m)^\s*(\d+)\s*/\s*\d+\s*$`)
	numberedHeadingRE = regexp.MustCompile(`^\s*(\d+(?:\.\d+)*)(?:[、．.]|\s|\p{Han})`)
	fontRE            = regexp.MustCompile(`(?:仿宋(?:_GB2312)?|宋体|黑体|楷体(?:_GB2312)?|微软雅黑|SimSun|SimHei|FangSong|KaiTi|TimesNewRoman|Arial|Calibri)`)
	sizeRE            = regexp.MustCompile(`(?:小[一二三四五六]号|初号|小初|[一二三四五六七八]号)`)
	pointSizeRE       = regexp.MustCompile(`(?:字号(?:为|采用|使用|[:：])?|字体大小(?:为|[:：])?)[“"']?(\d+(?:\.\d+)?)(?:磅|pt|PT)`)
	lineExactRE       = regexp.MustCompile(`(?:行[间]?距(?:为|采用|设置为|设为)?(?:固定值|固定)?(?:为)?|固定(?:值|行距)?)[：:]?(\d+(?:\.\d+)?)(?:磅|pt|PT)`)
	lineMultipleRE    = regexp.MustCompile(`行[间]?距(?:为|采用|设置为|设为)?[：:]?(\d+(?:\.\d+)?)倍`)
	indentRE          = regexp.MustCompile(`首行缩进(?:为|设置为|设为)?(\d+(?:\.\d+)?|[一二三四])(?:个)?字符`)
	marginRE          = regexp.MustCompile(`(上(?:边距)?|下(?:边距)?|左(?:边距)?|右(?:边距)?)(?:为|[:：]|要求)?(\d+(?:\.\d+)?)(厘米|cm|CM|毫米|mm|MM)`)
	allMarginRE       = regexp.MustCompile(`(?:上[、,，]?下[、,，]?左[、,，]?右|四[边周]|各边|上下左右)(?:边距)?(?:各|均|全部)?(?:为|[:：])?(\d+(?:\.\d+)?)(厘米|cm|CM|毫米|mm|MM)`)
	otherMarginRE     = regexp.MustCompile(`(?:其余|其他)(?:边距)?(?:均|各)?(?:为|[:：])?(\d+(?:\.\d+)?)(厘米|cm|CM|毫米|mm|MM)`)
	noDecorationRE    = regexp.MustCompile(`(?:不得|不准|禁止|不能|不允许|不)(?:出现|采用|使用|设置|设|有)?(?:任何)?(?:加粗|粗体|倾斜|斜体|下划线)`)
	formatCandidateRE = regexp.MustCompile(`(?:字体|字号|行[间]?距|页[面边]*边距|排版|首行缩进|页眉|页脚|页码|不设目录|不得设置目录|不得设目录|不设封底|不设空白页|文字.*对齐)`)
)

// Parse accepts only explicit text supplied by the caller. It does not perform
// retrieval, infer missing settings from document appearance, or use an LLM.
func Parse(sources []Source) Result {
	result := Result{Rules: []Rule{}, Issues: []Issue{}}
	for _, source := range sources {
		if strings.TrimSpace(source.Text) == "" {
			ref := source
			ref.Text = ""
			result.Issues = append(result.Issues, Issue{Code: "missing_source_text", Message: "本次招标文件缺少可读取的原文", Source: &ref})
			continue
		}
		if source.ID == "" && source.Name == "" {
			result.Issues = append(result.Issues, Issue{Code: "missing_source", Message: "格式要求缺少可定位的招标文件来源"})
			continue
		}
		if source.SHA256 == "" {
			hash := sha256.Sum256([]byte(source.Text))
			source.SHA256 = hex.EncodeToString(hash[:])
		}
		parseSource(&result, source)
	}
	deduplicate(&result)
	return result
}

type statement struct {
	text       string
	start, end int
}

func statements(text string) []statement {
	var result []statement
	start := 0
	for offset, char := range text {
		end := offset + len(string(char))
		boundary := char == '。' || char == '；' || char == ';'
		if char == '\n' {
			next := text[end:]
			line, _, _ := strings.Cut(next, "\n")
			boundary = numberedHeadingRE.MatchString(line) || isScopeHeading(compact(line)) || strings.HasPrefix(strings.TrimSpace(line), "---PHYSICAL PAGE")
		}
		if boundary {
			if strings.TrimSpace(text[start:end]) != "" {
				result = append(result, statement{text[start:end], start, end})
			}
			start = end
		}
	}
	if strings.TrimSpace(text[start:]) != "" {
		result = append(result, statement{text[start:], start, len(text)})
	}
	return result
}

func compact(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, text)
}

func isScopeHeading(text string) bool {
	return (strings.Contains(text, "技术") || strings.Contains(text, "商务")) &&
		(strings.Contains(text, "编制要求") || strings.Contains(text, "制作要求") || strings.Contains(text, "排版要求") || strings.Contains(text, "字体要求") || (len([]rune(text)) < 80 && strings.HasSuffix(strings.TrimRight(text, "：:"), "部分")))
}

func parseSource(result *Result, source Source) {
	scope := ScopeAll
	technicalPrefix := ""
	items := statements(source.Text)
	pages := sourcePages(source.Text)
	pageIndex, pageNumber := 0, source.Page
	// A standalone page/chunk can begin midway through a format section. Only
	// use the explicit heading or anonymous-volume statement on that same page.
	baseScope := ScopeAll
	if len([]rune(source.Text)) <= 4000 && hasTechnicalText(compact(source.Text)) && !strings.Contains(compact(source.Text), "商务标") && !strings.Contains(compact(source.Text), "商务部分") {
		baseScope = ScopeTechnical
	}
	for _, item := range items {
		for pageIndex < len(pages) && pages[pageIndex].offset <= item.end {
			pageNumber = pages[pageIndex].number
			pageIndex++
		}
		normal := compact(item.text)
		if normal == "" {
			continue
		}
		if technicalPrefix != "" {
			if number := headingNumber(item.text); number != "" && !strings.HasPrefix(number+".", technicalPrefix+".") {
				scope, technicalPrefix = ScopeAll, ""
			}
		}
		if isScopeHeading(normal) {
			if hasTechnicalText(normal) {
				scope = ScopeTechnical
			} else if strings.Contains(normal, "商务") {
				scope = ScopeBusiness
			}
			if scope == ScopeTechnical {
				technicalPrefix = ""
				technicalPrefix = headingNumber(item.text)
			}
		}
		currentScope := scope
		if currentScope == ScopeAll {
			currentScope = baseScope
		}
		if hasTechnicalText(normal) {
			currentScope = ScopeTechnical
		}
		if strings.Contains(normal, "商务") && !hasTechnicalText(normal) && isScopeHeading(normal) {
			currentScope = ScopeBusiness
			scope = ScopeBusiness
		}
		ref := source
		ref.Text = ""
		ref.Page = pageNumber
		quote := strings.TrimSpace(item.text)
		// Evidence can include a physical page marker but never the preceding
		// document. Keep the complete original statement around the requirement.
		if marker := strings.LastIndex(quote, "---PHYSICAL PAGE"); marker > 0 {
			quote = strings.TrimSpace(quote[marker:])
		}
		p := clauseParser{result: result, scope: currentScope, source: ref, quote: quote, text: normal}
		p.parse()
	}
}

func headingNumber(text string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if printedPageRE.MatchString(first) {
		return ""
	}
	if match := numberedHeadingRE.FindStringSubmatch(first); len(match) > 1 {
		return match[1]
	}
	return ""
}

func hasTechnicalText(text string) bool {
	return strings.Contains(text, "技术暗标") || strings.Contains(text, "技术标（暗标）") || strings.Contains(text, "技术标(暗标)") || strings.Contains(text, "技术部分暗标") || strings.Contains(text, "技术标部分（暗标）") || strings.Contains(text, "技术部分（暗标）") || strings.Contains(text, "技术部分(暗标)")
}

type pageMarker struct{ offset, number int }

func sourcePages(text string) []pageMarker {
	matches := physicalPageRE.FindAllStringSubmatchIndex(text, -1)
	// Physical PDF pages take precedence over printed page labels: tenders
	// commonly start numbering after their cover (physical 57, printed 56).
	if len(matches) == 0 {
		matches = printedPageRE.FindAllStringSubmatchIndex(text, -1)
	}
	pages := make([]pageMarker, 0, len(matches))
	for _, match := range matches {
		number, _ := strconv.Atoi(text[match[2]:match[3]])
		pages = append(pages, pageMarker{match[1], number})
	}
	return pages
}

type clauseParser struct {
	result      *Result
	scope       Scope
	source      Source
	quote, text string
	recognized  bool
}

func (p *clauseParser) add(target, property, value string) {
	p.recognized = true
	p.result.Rules = append(p.result.Rules, Rule{Scope: p.scope, Target: target, Property: property, Value: value, Source: p.source, Quote: p.quote})
}

func (p *clauseParser) issue(property, message string) {
	p.recognized = true
	ref := p.source
	p.result.Issues = append(p.result.Issues, Issue{Code: "unsupported", Scope: p.scope, Property: property, Message: message, Source: &ref, Quote: p.quote})
}

func (p *clauseParser) parse() {
	t := p.text
	// Do not treat a narrative mention of a document, bid cover sample, or a
	// general instruction to compile bids as a formatting requirement.
	if len([]rune(t)) > 2500 {
		return
	}
	if strings.Contains(t, "不显示供应商名称") || strings.Contains(t, "不得出现供应商") || strings.Contains(t, "不能出现涉及供应商名称") {
		if p.scope == ScopeTechnical {
			p.add("document", "anonymous", "true")
		}
	}
	if strings.Contains(t, "A4") && (strings.Contains(t, "纸张") || strings.Contains(t, "幅面") || strings.Contains(t, "版面要求")) {
		p.add("page", "paper", "A4")
	}
	if strings.Contains(t, "A3") && (strings.Contains(t, "纸张") || strings.Contains(t, "幅面")) {
		p.issue("page.paper", "招标要求包含 A3 或混合幅面，当前需人工确认版面")
	}
	p.parseFont()
	p.parseParagraph()
	p.parseMargins()
	p.parseStructure()
	if !p.recognized && formatCandidateRE.MatchString(t) && requiresFormat(t) && !isOnlyHeading(t) {
		p.issue("format", "招标文件存在尚未能可靠解析的格式要求，需核对原文")
	}
}

func requiresFormat(t string) bool {
	return strings.Contains(t, "要求") || strings.Contains(t, "采用") || strings.Contains(t, "均为") || strings.Contains(t, "应") || strings.Contains(t, "须") || strings.Contains(t, "不得") || strings.Contains(t, "不设") || strings.Contains(t, "不编设") || strings.Contains(t, "为")
}

func isOnlyHeading(t string) bool {
	return strings.HasSuffix(strings.TrimRight(t, "：:；;。"), "要求") || strings.HasSuffix(strings.TrimRight(t, "：:；;。"), "封面") || strings.HasSuffix(strings.TrimRight(t, "：:；;。"), "部分")
}

func (p *clauseParser) targets() []string {
	t := p.text
	if strings.Contains(t, "所有文字") || strings.Contains(t, "全部文字") || strings.Contains(t, "所有字体") {
		return []string{"all_text"}
	}
	if strings.Contains(t, "标题") && (strings.Contains(t, "正文") || strings.Contains(t, "正文字体")) {
		return []string{"body", "heading"}
	}
	if strings.Contains(t, "标题") {
		return []string{"heading"}
	}
	if strings.Contains(t, "表内") || strings.Contains(t, "表格内") || strings.Contains(t, "表格字体") {
		return []string{"table"}
	}
	return []string{"body"}
}

func (p *clauseParser) parseFont() {
	t := p.text
	if strings.Contains(t, "字体及字号不作要求") || strings.Contains(t, "字体和字号不作要求") || strings.Contains(t, "字号不作要求") {
		p.recognized = true
		return
	}
	fontClause := strings.Contains(t, "字体") || strings.Contains(t, "字号") || strings.Contains(t, "文字均采用") || strings.Contains(t, "正文字") ||
		((strings.Contains(t, "正文") || strings.Contains(t, "标题")) && (strings.Contains(t, "采用") || strings.Contains(t, "使用") || sizeRE.MatchString(t)))
	if fontClause && (fontRE.MatchString(t) || sizeRE.MatchString(t)) &&
		(strings.Contains(t, "不得采用") || strings.Contains(t, "不得使用") || strings.Contains(t, "禁止采用") || strings.Contains(t, "禁止使用") || strings.Contains(t, "不能采用") || strings.Contains(t, "不能使用")) {
		p.issue("font_exclusion", "招标规定禁止使用的字体或字号，需确认允许的样式后生成")
		fontClause = false
	}
	if fontClause && (strings.Contains(t, "可采用") || strings.Contains(t, "可以采用") || strings.Contains(t, "可选择") || strings.Contains(t, "建议使用") || strings.Contains(t, "推荐使用")) && (fontRE.MatchString(t) || sizeRE.MatchString(t)) {
		p.issue("font_choices", "招标列出可选或建议样式，未将其中一种推断为强制要求")
		fontClause = false
	}
	if fontClause {
		fonts := fontRE.FindAllString(t, -1)
		if len(fonts) > 0 {
			fonts = unique(fonts)
			if len(fonts) == 1 {
				font := fonts[0]
				if font == "TimesNewRoman" {
					font = "Times New Roman"
				}
				for _, target := range p.targets() {
					p.add(target, "font_family", font)
				}
			} else {
				p.issue("font_family", "同一句要求包含多种字体，需确认各自适用位置")
			}
		} else if (strings.Contains(t, "采用") || strings.Contains(t, "字体为") || strings.Contains(t, "字体：")) && !strings.Contains(t, "颜色") && !hasNamedColor(t) && !strings.Contains(t, "常规") && !strings.Contains(t, "间距") {
			p.issue("font_family", "字体名称尚未支持可靠解析，需核对招标原文")
		}
		if sizes := unique(sizeRE.FindAllString(t, -1)); len(sizes) > 0 {
			if len(sizes) > 1 {
				p.issue("font_size_half_points", "同一句要求包含多个字号，需确认各自适用位置")
			} else {
				for _, target := range p.targets() {
					p.add(target, "font_size_half_points", strconv.Itoa(chineseSizes[sizes[0]]))
				}
			}
		} else if size := pointSizeRE.FindStringSubmatch(t); len(size) > 1 {
			value, _ := strconv.ParseFloat(size[1], 64)
			if value >= 5 && value <= 72 {
				for _, target := range p.targets() {
					p.add(target, "font_size_half_points", strconv.Itoa(int(math.Round(value*2))))
				}
			} else {
				p.issue("font_size_half_points", "字号超出当前支持范围")
			}
		}
	}
	if strings.Contains(t, "字体") || strings.Contains(t, "文字") || strings.Contains(t, "图表") || strings.Contains(t, "颜色") {
		targets := p.targets()
		if strings.Contains(t, "所有") || strings.Contains(t, "均为") {
			targets = []string{"all_text"}
		}
		var colors []string
		for _, color := range namedColors {
			if strings.Contains(t, color.name) {
				colors = append(colors, color.value)
			}
		}
		if len(colors) > 1 {
			p.issue("color", "同一句要求包含多种文字颜色，需确认各自适用位置")
		} else if len(colors) == 1 {
			for _, target := range targets {
				p.add(target, "color", colors[0])
			}
		} else if strings.Contains(t, "颜色") && requiresFormat(t) && !isOnlyHeading(t) {
			p.issue("color", "尚不能可靠解析招标规定的文字颜色")
		}
	}
	// A single prohibition often lists several decorations after its first verb.
	if noDecorationRE.MatchString(t) || (strings.Contains(t, "不得") && strings.Contains(t, "标记")) {
		target := "all_text"
		if strings.Contains(t, "加粗") || strings.Contains(t, "粗体") {
			p.add(target, "bold", "false")
		}
		if strings.Contains(t, "倾斜") || strings.Contains(t, "斜体") {
			p.add(target, "italic", "false")
		}
		if strings.Contains(t, "下划线") {
			p.add(target, "underline", "false")
		}
		if strings.Contains(t, "加色") {
			p.add(target, "color", "000000")
		}
	} else if strings.Contains(t, "常规") && (strings.Contains(t, "字体") || strings.Contains(t, "文字")) {
		for _, target := range p.targets() {
			p.add(target, "bold", "false")
			p.add(target, "italic", "false")
			p.add(target, "underline", "false")
		}
	}
	if strings.Contains(t, "字体") && (strings.Contains(t, "加粗") || strings.Contains(t, "粗体")) && !strings.Contains(t, "不得") && !strings.Contains(t, "禁止") && !strings.Contains(t, "不加粗") {
		for _, target := range p.targets() {
			p.add(target, "bold", "true")
		}
	}
	if strings.Contains(t, "全部使用中文标点") {
		p.issue("content.punctuation", "招标要求全部使用中文标点，需检查生成正文和表格内容")
	}
	if strings.Contains(t, "不得有底色") || strings.Contains(t, "无底色") {
		p.add("all_text", "shading", "false")
	}
}

var namedColors = []struct{ name, value string }{{"黑色", "000000"}, {"红色", "FF0000"}, {"蓝色", "0000FF"}, {"绿色", "008000"}, {"白色", "FFFFFF"}}

func hasNamedColor(t string) bool {
	for _, color := range namedColors {
		if strings.Contains(t, color.name) {
			return true
		}
	}
	return false
}

var chineseSizes = map[string]int{"初号": 84, "小初": 72, "一号": 52, "小一号": 48, "二号": 44, "小二号": 36, "三号": 32, "小三号": 30, "四号": 28, "小四号": 24, "五号": 21, "小五号": 18, "六号": 15, "小六号": 13, "七号": 11, "八号": 10}

func (p *clauseParser) parseParagraph() {
	t := p.text
	if strings.Contains(t, "行距") || strings.Contains(t, "行间距") {
		if match := lineExactRE.FindStringSubmatch(t); len(match) > 1 {
			value, _ := strconv.ParseFloat(match[1], 64)
			p.add("body", "line_spacing_twips", strconv.Itoa(int(math.Round(value*20))))
			p.add("body", "line_rule", "exact")
		} else if match := lineMultipleRE.FindStringSubmatch(t); len(match) > 1 {
			value, _ := strconv.ParseFloat(match[1], 64)
			p.add("body", "line_spacing_twips", strconv.Itoa(int(math.Round(value*240))))
			p.add("body", "line_rule", "auto")
		} else if strings.Contains(t, "单倍行距") {
			p.add("body", "line_spacing_twips", "240")
			p.add("body", "line_rule", "auto")
		} else if strings.Contains(t, "双倍行距") {
			p.add("body", "line_spacing_twips", "480")
			p.add("body", "line_rule", "auto")
		} else {
			p.issue("body.line_spacing", "无法可靠解析招标规定的行距")
		}
	}
	for _, alignment := range []struct{ phrase, value string }{{"左对齐", "left"}, {"居中", "center"}, {"右对齐", "right"}, {"两端对齐", "both"}} {
		if strings.Contains(t, alignment.phrase) {
			targets := p.targets()
			if strings.Contains(t, "统一") || strings.Contains(t, "文字内容") {
				targets = []string{"body", "heading"}
			}
			for _, target := range targets {
				p.add(target, "alignment", alignment.value)
			}
		}
	}
	if match := indentRE.FindStringSubmatch(t); len(match) > 1 {
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			value = map[string]float64{"一": 1, "二": 2, "三": 3, "四": 4}[match[1]]
		}
		p.add("body", "first_line_chars", strconv.Itoa(int(math.Round(value*100))))
		// In an anonymous text section the adjacent universal alignment rule
		// explicitly includes titles, so indentation also applies to titles.
		if p.scope == ScopeTechnical {
			p.add("heading", "first_line_chars", strconv.Itoa(int(math.Round(value*100))))
		}
	}
	if strings.Contains(t, "段落前后不设置空行") || strings.Contains(t, "段前段后均为0") || strings.Contains(t, "段前段后为0") {
		p.add("body", "space_before_twips", "0")
		p.add("body", "space_after_twips", "0")
		if p.scope == ScopeTechnical {
			p.add("heading", "space_before_twips", "0")
			p.add("heading", "space_after_twips", "0")
		}
	}
	if strings.Contains(t, "字符间距") || strings.Contains(t, "字符位置") {
		if strings.Contains(t, "为标准") || strings.Contains(t, "标准间距") || strings.Contains(t, "间距为0") {
			p.add("all_text", "character_spacing_twips", "0")
			if strings.Contains(t, "位置") {
				p.add("all_text", "position_half_points", "0")
			}
		} else {
			p.issue("all_text.character_spacing", "尚不能可靠解析招标规定的字符间距或位置")
		}
	}
}

func (p *clauseParser) parseMargins() {
	t := p.text
	if !strings.Contains(t, "边距") {
		return
	}
	sides := map[string]string{"上": "margin_top_twips", "下": "margin_bottom_twips", "左": "margin_left_twips", "右": "margin_right_twips"}
	seen := make(map[string]bool)
	if match := allMarginRE.FindStringSubmatch(t); len(match) > 1 {
		value := lengthTwips(match[1], match[2])
		for _, side := range []string{"上", "下", "左", "右"} {
			p.add("page", sides[side], strconv.Itoa(value))
			seen[side] = true
		}
	}
	for _, match := range marginRE.FindAllStringSubmatch(t, -1) {
		side := string([]rune(match[1])[0])
		p.add("page", sides[side], strconv.Itoa(lengthTwips(match[2], match[3])))
		seen[side] = true
	}
	if match := otherMarginRE.FindStringSubmatch(t); len(match) > 1 {
		value := lengthTwips(match[1], match[2])
		for _, side := range []string{"上", "下", "左", "右"} {
			if !seen[side] {
				p.add("page", sides[side], strconv.Itoa(value))
				seen[side] = true
			}
		}
	}
	if len(seen) == 0 {
		p.issue("page.margins", "无法可靠解析招标规定的页面边距")
	}
}

func lengthTwips(number, unit string) int {
	value, _ := strconv.ParseFloat(number, 64)
	if unit == "厘米" || strings.EqualFold(unit, "cm") {
		value *= 10
	}
	return int(math.Round(value / 25.4 * 1440))
}

func (p *clauseParser) parseStructure() {
	t := p.text
	if strings.Contains(t, "需自行编制页码") || strings.Contains(t, "须自行编制页码") {
		if p.scope == ScopeAll {
			p.issue("page.page_numbers_scope", "主目录要求自行编制页码，但未明确适用分册，需结合暗标的局部例外核对")
		} else {
			p.add("page", "page_numbers", "true")
		}
	}
	negative := strings.Contains(t, "不得设置") || strings.Contains(t, "不得设") || strings.Contains(t, "不设置") || strings.Contains(t, "不设") || strings.Contains(t, "不编设") || strings.Contains(t, "无需体现")
	if negative {
		for _, feature := range []struct{ word, target, property string }{{"目录", "structure", "toc"}, {"页眉", "page", "header"}, {"页脚", "page", "footer"}, {"页码", "page", "page_numbers"}, {"封底", "structure", "back_cover"}, {"空白页", "structure", "blank_pages"}} {
			if strings.Contains(t, feature.word) {
				p.add(feature.target, feature.property, "false")
			}
		}
		if strings.Contains(t, "不得设置封面") || strings.Contains(t, "不得设封面") || strings.Contains(t, "不设置封面") || strings.Contains(t, "不设封面") || (strings.Contains(t, "封面") && strings.Contains(t, "无需体现")) {
			p.add("structure", "cover", "false")
		}
	}
	if strings.Contains(t, "封面") && (strings.Contains(t, "给定的格式") || strings.Contains(t, "规定格式") || strings.Contains(t, "指定格式")) {
		p.add("structure", "cover", "true")
		p.issue("structure.cover_template", "招标要求套用指定封面，需确认并提供原始封面模板，不能套用通用封面")
	}
	if strings.Contains(t, "不得有空格") {
		p.issue("content.spaces", "招标要求不得有空格，需核对正文内容，不能自动删除产品型号中的空格")
	}
	if strings.Contains(t, "无空白页") {
		p.add("structure", "blank_pages", "false")
	}
}

func unique(values []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func deduplicate(result *Result) {
	seen := make(map[string]bool)
	rules := result.Rules[:0]
	for _, rule := range result.Rules {
		key := string(rule.Scope) + "\x00" + rule.Target + "\x00" + rule.Property + "\x00" + rule.Value + "\x00" + rule.Source.ID + "\x00" + rule.Source.SHA256 + "\x00" + strconv.Itoa(rule.Source.Page) + "\x00" + rule.Quote
		if !seen[key] {
			seen[key] = true
			rules = append(rules, rule)
		}
	}
	result.Rules = rules
}
