package bidcheck

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/Tencent/WeKnora/internal/bidformat"
)

var identityCandidateRE = regexp.MustCompile(`(?:[\p{Han}A-Za-z0-9（）()·]{2,40}(?:有限责任公司|股份有限公司|有限公司)|[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}|(?:联系人|法定代表人|联系电话|统一社会信用代码)\s*[:：]\s*\S{2,40})`)
var pageMarkerRE = regexp.MustCompile(`^\s*---PHYSICAL PAGE\s+(\d+)---\s*$`)
var anonymousProhibitionRE = regexp.MustCompile(`(?:不得|禁止|不能|不允许|不准)(?:出现|包含|含有|体现|显示|标注|有)[^。；;\n]{0,40}(?:(?:投标人|供应商|企业|单位)名称|识别(?:投标人|供应商)|(?:投标人|供应商)身份)|不显示(?:投标人|供应商|企业|单位)名称`)

// Analyze is deterministic and uses only caller-provided evidence. A pass is
// limited to machine-verifiable requirements; a review is never counted as one.
func Analyze(req Request) (*Report, error) {
	if req.Scope == "" {
		req.Scope = bidformat.ScopeTechnical
	}
	if req.Scope != bidformat.ScopeAll && req.Scope != bidformat.ScopeBusiness && req.Scope != bidformat.ScopeTechnical {
		return nil, fmt.Errorf("无效的标书分册")
	}
	if strings.TrimSpace(req.Tender.Name) == "" || strings.TrimSpace(req.Bid.Name) == "" {
		return nil, fmt.Errorf("请提供招标文件和标书文件名称")
	}
	var facts *docFacts
	var err error
	if strings.EqualFold(req.Bid.Format, "docx") {
		facts, err = inspectDOCX(req.Bid.Data)
		if err != nil {
			return nil, err
		}
		// The actual package is authoritative, even when text supplied by a
		// separate parser omits revisions, tables, or hidden runs.
		var lines []string
		for _, p := range facts.paragraphs {
			lines = append(lines, p.text)
		}
		req.Bid.Text = strings.Join(lines, "\n")
	}
	if strings.EqualFold(req.Tender.Format, "docx") && len(req.Tender.Data) > 0 {
		req.Tender.Text, err = ExtractDOCXText(req.Tender.Data)
		if err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(req.Tender.Text) == "" || strings.TrimSpace(req.Bid.Text) == "" {
		return nil, fmt.Errorf("招标文件或标书没有可读取的正文；扫描件需先完成 OCR")
	}
	r := &Report{TenderFile: req.Tender.Name, BidFile: req.Bid.Name, Scope: string(req.Scope), Checks: []Check{}, Rules: []Rule{}, Limitations: []string{}}
	r.add(Check{Category: "requirements", Title: "招标原文覆盖范围需复核", Status: "review", Message: "规则来自可提取的招标正文；扫描图像、附件、脚注或解析未覆盖的内容可能包含其他要求。", SourceFile: req.Tender.Name, Suggestion: "对照完整招标文件确认暗标条款和补充公告已覆盖，检查报告不能替代人工评审。"})
	if strings.EqualFold(req.Tender.Format, "docx") && len(req.Tender.Data) > 0 {
		tf, e := inspectDOCX(req.Tender.Data)
		if e != nil {
			return nil, e
		}
		if tf.revisions > 0 {
			r.add(Check{Category: "requirements", Title: "招标修订版本需确认", Status: "review", Message: "招标文件包含修订记录，已删除文字不作为当前标准；请确认最终有效版本。", SourceFile: req.Tender.Name, Suggestion: "接受或拒绝修订并取得确认后的招标版本，再重新检查。"})
		}
	}
	parsed := bidformat.Parse([]bidformat.Source{{ID: "uploaded-tender", Name: req.Tender.Name, Text: req.Tender.Text}})
	if !req.Tender.VerifiedPages {
		for i := range parsed.Rules {
			parsed.Rules[i].Source.Page = 0
		}
		for i := range parsed.Issues {
			if parsed.Issues[i].Source != nil {
				parsed.Issues[i].Source.Page = 0
			}
			for j := range parsed.Issues[i].Rules {
				parsed.Issues[i].Rules[j].Source.Page = 0
			}
		}
	}
	spec, issues := parsed.Resolve(req.Scope)
	for _, rule := range parsed.Rules {
		if rule.Scope != bidformat.ScopeAll && rule.Scope != req.Scope {
			continue
		}
		r.Rules = append(r.Rules, Rule{ID: fmt.Sprintf("rule-%d", len(r.Rules)+1), Category: rule.Target, Requirement: rule.Quote, SourceFile: rule.Source.Name, SourcePage: rule.Source.Page})
	}
	for _, issue := range issues {
		c := Check{Category: "requirements", Title: "招标规则需复核", Status: "review", Message: strings.ReplaceAll(issue.Message, "后生成", "后检查"), Suggestion: "核对招标原文并确认该项实际适用要求。", SourceFile: req.Tender.Name, Requirement: issue.Quote}
		if issue.Source != nil {
			c.SourceFile = issue.Source.Name
			c.SourcePage = issue.Source.Page
		}
		if len(issue.Rules) > 0 {
			c.Requirement = joinQuotes(issue.Rules)
			c.SourceFile = issue.Rules[0].Source.Name
			c.SourcePage = issue.Rules[0].Source.Page
		}
		r.add(c)
	}
	if req.Scope == bidformat.ScopeAll {
		r.Limitations = append(r.Limitations, "本次范围为通用要求，不包含技术或商务分册专用要求；分册应分别上传检查。")
		for _, rule := range parsed.Rules {
			if rule.Scope != bidformat.ScopeAll {
				r.add(Check{Category: "requirements", Title: "分册要求需单独检查", Status: "review", Message: "招标文件存在技术或商务分册专用要求；当前仅对通用要求核对。", Requirement: rule.Quote, SourceFile: rule.Source.Name, SourcePage: rule.Source.Page, Suggestion: "分别选择技术分册或商务分册，上传对应分册检查。"})
				break
			}
		}
	}
	seen := map[string]bool{}
	for _, rule := range spec.Rules {
		key := rule.Target + "." + rule.Property
		if seen[key] || key == "document.anonymous" {
			continue
		}
		seen[key] = true
		c := Check{Category: rule.Target, Title: propertyTitle(rule.Target, rule.Property), Status: "review", Requirement: rule.Quote, SourceFile: rule.Source.Name, SourcePage: rule.Source.Page, Suggestion: "在标书原文件中核对此项，并按招标原文修改。"}
		if facts == nil {
			c.Message = "当前文件只能核对提取文本，无法据此确认原始排版、页眉页脚或成稿结构。"
		} else {
			applyFormatCheck(&c, rule, facts)
		}
		r.add(c)
	}
	var anonymousRule *bidformat.Rule
	for _, rule := range spec.Rules {
		if rule.Target == "document" && rule.Property == "anonymous" && rule.Value == "true" {
			if clause, ok := explicitAnonymousClause(rule.Quote); ok {
				cp := rule
				cp.Quote = clause
				anonymousRule = &cp
				break
			}
			r.add(Check{Category: "requirements", Title: "匿名要求适用条件需确认", Status: "review", Message: "该身份条款含限定、例外或无法可靠关联的禁止对象，未将所有身份关键词直接判为违规。", Requirement: rule.Quote, SourceFile: rule.Source.Name, SourcePage: rule.Source.Page, Suggestion: "核对条款禁止的具体身份对象及允许位置。"})
		}
	}
	// bidformat supports a narrow vocabulary. Additional explicit anonymity
	// clauses are retained verbatim rather than silently skipped.
	if anonymousRule == nil {
		anonymousRule = findAnonymousRequirement(req.Tender.Text, req.Tender.Name, req.Scope)
	}
	if anonymousRule != nil && !req.Tender.VerifiedPages {
		anonymousRule.Source.Page = 0
	}
	if anonymousRule != nil {
		if exception := anonymousException(req.Tender.Text, req.Scope); exception != "" {
			r.add(Check{Category: "requirements", Title: "匿名条款存在允许位置或例外", Status: "review", Message: "招标原文同时包含身份信息的允许位置或例外，自动检查无法可靠确认所有命中位置的适用范围。", Requirement: anonymousRule.Quote + "\n" + exception, SourceFile: req.Tender.Name, Suggestion: "逐条确认禁止范围及允许的封面、分册或其他例外位置。"})
			anonymousRule = nil
		}
	}
	if anonymousRule != nil && !hasAnonymousRule(r.Rules, anonymousRule.Quote) {
		r.Rules = append(r.Rules, Rule{ID: fmt.Sprintf("rule-%d", len(r.Rules)+1), Category: "document", Requirement: anonymousRule.Quote, SourceFile: anonymousRule.Source.Name, SourcePage: anonymousRule.Source.Page})
	}
	checkIdentities(r, req, facts, anonymousRule)
	if len(r.Rules) == 0 {
		r.add(Check{Category: "requirements", Title: "未确认适用暗标标准", Status: "review", Message: "未从招标文件中识别到所选分册可自动核对的明确要求。", SourceFile: req.Tender.Name, Suggestion: "核对分册选择和暗标条款，确认原文件文字已完整提取。"})
	}
	if facts == nil {
		r.Limitations = append(r.Limitations, "当前上传格式仅支持提取文本检查；字体、字号、页边距、图片、页眉页脚、批注修订和文件作者信息需核对原始文件。")
		r.add(Check{Category: "document", Title: "原始格式与图片需复核", Status: "review", Message: "文本提取无法证明 PDF / DOC / 文本文件的实际格式和图片符合招标要求。", Suggestion: "优先上传原始 DOCX；仍需人工核对最终成稿或扫描图像。"})
	} else {
		checkPackage(r, facts, anonymousRule)
		r.Limitations = append(r.Limitations, "DOCX 检查读取原始 XML 和样式，不渲染 Word 页面；实际分页、封面封底、空白页、手工目录及图片中的文字、Logo、印章仍需人工复核。")
		r.add(Check{Category: "visual", Title: "最终成稿人工复核", Status: "review", Message: "自动检查无法完整覆盖招标原文中的视觉与语义要求。", Suggestion: "对照报告所列招标原文，人工检查最终提交文件、图片及所有未识别条款。"})
	}
	r.finish()
	return r, nil
}

func (r *Report) add(c Check) {
	c.ID = fmt.Sprintf("check-%d", len(r.Checks)+1)
	r.Checks = append(r.Checks, c)
}
func (r *Report) finish() {
	r.Status = "pass"
	for _, c := range r.Checks {
		switch c.Status {
		case "fail":
			r.Summary.Failed++
		case "review":
			r.Summary.Review++
		case "pass":
			r.Summary.Passed++
		}
	}
	if r.Summary.Review > 0 {
		r.Status = "review"
	}
	if r.Summary.Failed > 0 {
		r.Status = "fail"
	}
}
func joinQuotes(rules []bidformat.Rule) string {
	var result []string
	seen := map[string]bool{}
	for _, rule := range rules {
		if !seen[rule.Quote] {
			result = append(result, rule.Quote)
			seen[rule.Quote] = true
		}
	}
	return strings.Join(result, "\n")
}
func propertyTitle(target, property string) string {
	targets := map[string]string{"body": "正文", "heading": "标题", "table": "表格", "all_text": "全篇文字", "page": "页面", "structure": "文档结构"}
	props := map[string]string{"font_family": "字体", "font_size_half_points": "字号", "color": "字体颜色", "bold": "加粗", "italic": "斜体", "underline": "下划线", "shading": "底纹", "character_spacing_twips": "字符间距", "position_half_points": "字符位置", "line_spacing_twips": "行距", "line_rule": "行距类型", "alignment": "对齐", "first_line_chars": "首行缩进", "space_before_twips": "段前间距", "space_after_twips": "段后间距", "paper": "纸张", "margin_top_twips": "上边距", "margin_bottom_twips": "下边距", "margin_left_twips": "左边距", "margin_right_twips": "右边距", "header": "页眉", "footer": "页脚", "page_numbers": "页码", "cover": "封面", "toc": "目录", "back_cover": "封底", "blank_pages": "空白页"}
	return targets[target] + props[property]
}

type observed struct{ value, location, excerpt string }

func applyFormatCheck(c *Check, rule bidformat.Rule, f *docFacts) {
	var values []observed
	switch rule.Target {
	case "page":
		switch rule.Property {
		case "header", "footer":
			if f.missingParts {
				c.Message = "页眉或页脚引用缺失，无法确认最终页面内容。"
				return
			}
			present := false
			kind := rule.Property
			active := f.activeHeaders
			if kind == "footer" {
				active = f.activeFooters
			}
			for name, t := range f.auxiliary {
				if active[name] && strings.TrimSpace(t) != "" {
					present = true
					values = append(values, observed{"true", kind, short(t)})
				}
			}
			// An image-only header/footer also contains visible content.
			for name, n := range f.xmls {
				if active[name] {
					n.walkActive(func(cn *xmlNode) {
						if cn.name == "drawing" || cn.name == "pict" || cn.name == "fldSimple" || cn.name == "instrText" {
							present = true
						}
					})
				}
			}
			if len(values) == 0 {
				values = append(values, observed{strconv.FormatBool(present), kind, ""})
			}
		case "page_numbers":
			found := false
			for name, n := range f.xmls {
				if name == "word/document.xml" || f.activeHeaders[name] || f.activeFooters[name] {
					n.walkActive(func(cn *xmlNode) {
						text := cn.text
						if cn.name == "fldSimple" {
							text = cn.attr("instr")
						}
						if (cn.name == "instrText" || cn.name == "fldSimple") && isPageField(text) {
							found = true
							values = append(values, observed{"true", name, "PAGE 页码域"})
						}
					})
				}
			}
			if !found {
				c.Message = "未检测到 PAGE 页码域；纯文本或图形形式的手工页码无法可靠识别。"
				return
			}
		default:
			for i, s := range f.sections {
				values = append(values, observed{s[rule.Property], fmt.Sprintf("第 %d 节页面设置", i+1), ""})
			}
		}
	case "structure":
		if rule.Property == "toc" && f.toc {
			values = append(values, observed{"true", "文档目录域", "TOC 目录域"})
		} else {
			c.Message = "该项需要结合 Word 实际分页或手工内容人工核对；XML 不能证明封面、封底、空白页或手工目录不存在。"
			return
		}
	case "body", "heading", "table", "all_text":
		if rule.Target == "all_text" {
			for name, text := range f.auxiliary {
				if strings.TrimSpace(text) != "" && (f.activeHeaders[name] || f.activeFooters[name] || name == "word/footnotes.xml" || name == "word/endnotes.xml") {
					c.Message = "全篇要求涉及页眉页脚、脚注或尾注；当前仅解析正文样式，需核对非正文文字。"
					return
				}
			}
		}
		paragraphProperty := isParagraphProperty(rule.Property)
		for _, p := range f.paragraphs {
			if rule.Target != "all_text" && rule.Target != p.target {
				continue
			}
			if paragraphProperty {
				values = append(values, observed{p.props[rule.Property], p.location, short(p.text)})
				continue
			}
			for _, run := range p.runs {
				if rule.Property == "font_family" {
					if hasHan(run.text) {
						values = append(values, observed{run.props["font_east"], p.location, short(run.text)})
					}
					if hasLatin(run.text) {
						values = append(values, observed{run.props["font_ascii"], p.location, short(run.text)})
					}
					if !hasHan(run.text) && !hasLatin(run.text) {
						values = append(values, observed{run.props["font_east"], p.location, short(run.text)})
					}
				} else {
					values = append(values, observed{run.props[rule.Property], p.location, short(run.text)})
				}
			}
		}
	default:
		c.Message = "该要求尚不能可靠自动核对。"
		return
	}
	if len(values) == 0 {
		c.Message = "没有足够的可定位内容或格式属性，无法确认该要求是否满足。"
		return
	}
	unknown := 0
	for _, v := range values {
		if v.value == "" || v.value == "?" {
			unknown++
			continue
		}
		if !equivalent(rule.Property, v.value, rule.Value) {
			c.Status = "fail"
			c.Message = fmt.Sprintf("检测到不符合要求的格式：实际值 %s，要求值 %s。", displayValue(rule.Property, v.value), displayValue(rule.Property, rule.Value))
			c.Location = v.location
			c.Excerpt = v.excerpt
			return
		}
	}
	if unknown > 0 {
		c.Message = fmt.Sprintf("已核对 %d 项属性，其中 %d 项无法从样式或原始 XML 中确定。", len(values)-unknown, unknown)
		return
	}
	c.Status = "pass"
	c.Message = fmt.Sprintf("核对的 %d 项可读取属性符合这条明确要求。", len(values))
	c.Suggestion = ""
	c.Location = values[0].location
}
func isParagraphProperty(p string) bool {
	switch p {
	case "line_spacing_twips", "line_rule", "alignment", "first_line_chars", "space_before_twips", "space_after_twips":
		return true
	}
	return false
}
func hasLatin(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Latin, r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
func equivalent(property, a, b string) bool {
	if property == "font_family" {
		return normalizedFont(a) == normalizedFont(b)
	}
	if strings.HasSuffix(property, "_twips") {
		ai, e1 := strconv.Atoi(a)
		bi, e2 := strconv.Atoi(b)
		if e1 == nil && e2 == nil {
			return abs(ai-bi) <= 2
		}
	}
	return strings.EqualFold(a, b)
}
func normalizedFont(s string) string {
	s = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	for _, pair := range [][2]string{{"simsun", "宋体"}, {"simhei", "黑体"}, {"fangsong", "仿宋"}, {"kaiti", "楷体"}, {"microsoftyahei", "微软雅黑"}} {
		if s == pair[0] {
			return pair[1]
		}
	}
	return s
}
func displayValue(property, value string) string {
	labels := map[string][2]string{
		"bold": {"常规", "加粗"}, "italic": {"不倾斜", "倾斜"},
		"underline": {"无下划线", "有下划线"}, "shading": {"无底纹", "有底纹"},
		"header": {"无页眉", "有页眉"}, "footer": {"无页脚", "有页脚"},
		"page_numbers": {"无页码", "有页码"}, "toc": {"无目录", "有目录"},
	}
	if options, ok := labels[property]; ok {
		if value == "false" {
			return options[0]
		}
		if value == "true" {
			return options[1]
		}
	}
	if property == "font_size_half_points" {
		if n, e := strconv.Atoi(value); e == nil {
			return fmt.Sprintf("%.1f 磅", float64(n)/2)
		}
	}
	if strings.HasPrefix(property, "margin_") && strings.HasSuffix(property, "_twips") {
		if n, e := strconv.Atoi(value); e == nil {
			return fmt.Sprintf("%.2f 厘米", float64(n)*2.54/1440)
		}
	} else if strings.HasSuffix(property, "_twips") {
		if n, e := strconv.Atoi(value); e == nil {
			return fmt.Sprintf("%.1f 磅", float64(n)/20)
		}
	}
	if property == "line_rule" {
		if value == "exact" {
			return "固定行距"
		}
		if value == "auto" {
			return "自动行距"
		}
	}
	return value
}
func isPageField(s string) bool {
	parts := strings.Fields(strings.ToUpper(strings.TrimSpace(s)))
	return len(parts) > 0 && (parts[0] == "PAGE" || parts[0] == "NUMPAGES")
}
func short(s string) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) > 160 {
		return string(runes[:160]) + "…"
	}
	return string(runes)
}
func matchExcerpt(text, match string) string {
	lower := []rune(strings.ToLower(text))
	target := []rune(strings.ToLower(match))
	index := -1
	for i := 0; i+len(target) <= len(lower); i++ {
		if string(lower[i:i+len(target)]) == string(target) {
			index = i
			break
		}
	}
	if index < 0 {
		return short(text)
	}
	original := []rune(text)
	before := original[:index]
	after := original[index:]
	prefix := ""
	suffix := ""
	if len(before) > 60 {
		before = before[len(before)-60:]
		prefix = "…"
	}
	if len(after) > 100 {
		after = after[:100]
		suffix = "…"
	}
	return prefix + string(before) + string(after) + suffix
}

func hasAnonymousRule(rules []Rule, quote string) bool {
	for _, r := range rules {
		if r.Category == "document" && r.Requirement == quote {
			return true
		}
	}
	return false
}
func findAnonymousRequirement(text, name string, scope bidformat.Scope) *bidformat.Rule {
	page := 0
	current := bidformat.ScopeAll
	for _, line := range strings.Split(text, "\n") {
		if m := pageMarkerRE.FindStringSubmatch(line); m != nil {
			page, _ = strconv.Atoi(m[1])
			continue
		}
		t := strings.TrimSpace(line)
		compact := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, t)
		if strings.Contains(compact, "技术") && (strings.Contains(compact, "暗标") || strings.Contains(compact, "制作要求") || strings.Contains(compact, "编制要求")) {
			current = bidformat.ScopeTechnical
		}
		if strings.Contains(compact, "商务") && (strings.Contains(compact, "制作要求") || strings.Contains(compact, "编制要求")) {
			current = bidformat.ScopeBusiness
		}
		if current != bidformat.ScopeAll && current != scope {
			continue
		}
		if clause, ok := explicitAnonymousClause(t); ok {
			return &bidformat.Rule{Scope: current, Target: "document", Property: "anonymous", Value: "true", Quote: clause, Source: bidformat.Source{Name: name, Page: page}}
		}
	}
	return nil
}

func explicitAnonymousClause(text string) (string, bool) {
	for _, clause := range strings.FieldsFunc(text, func(r rune) bool { return r == '。' || r == '；' || r == ';' || r == '\n' }) {
		compact := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, clause)
		if strings.Contains(compact, "允许") && !strings.Contains(compact, "不允许") || strings.Contains(compact, "可以") || strings.Contains(compact, "但") || strings.Contains(compact, "除外") || strings.Contains(compact, "除") || strings.Contains(compact, "例外") {
			continue
		}
		if anonymousProhibitionRE.MatchString(compact) {
			return strings.TrimSpace(clause), true
		}
	}
	return "", false
}

func anonymousException(text string, scope bidformat.Scope) string {
	current := bidformat.ScopeAll
	for _, line := range strings.Split(text, "\n") {
		compact := strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, line)
		if strings.Contains(compact, "技术") && (strings.Contains(compact, "暗标") || strings.Contains(compact, "制作要求") || strings.Contains(compact, "编制要求")) {
			current = bidformat.ScopeTechnical
		}
		if strings.Contains(compact, "商务") && (strings.Contains(compact, "制作要求") || strings.Contains(compact, "编制要求")) {
			current = bidformat.ScopeBusiness
		}
		if current != bidformat.ScopeAll && current != scope {
			continue
		}
		for _, clause := range strings.FieldsFunc(line, func(r rune) bool { return r == '。' || r == '；' || r == ';' }) {
			c := strings.Map(func(r rune) rune {
				if unicode.IsSpace(r) {
					return -1
				}
				return r
			}, clause)
			identity := strings.Contains(c, "投标人名称") || strings.Contains(c, "供应商名称") || strings.Contains(c, "企业名称") || strings.Contains(c, "单位名称") || strings.Contains(c, "身份")
			allow := strings.Contains(c, "允许") && !strings.Contains(c, "不允许") || strings.Contains(c, "可以") || strings.Contains(c, "可出现") || strings.Contains(c, "可显示") || strings.Contains(c, "除外") || strings.Contains(c, "例外")
			if identity && allow {
				return strings.TrimSpace(clause)
			}
		}
	}
	return ""
}

type textPart struct{ text, location, kind string }

func textParts(req Request, f *docFacts) []textPart {
	var parts []textPart
	if f != nil {
		for _, p := range f.paragraphs {
			parts = append(parts, textPart{p.text, p.location, p.target})
		}
		names := make([]string, 0, len(f.auxiliary))
		for n := range f.auxiliary {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if strings.TrimSpace(f.auxiliary[n]) != "" {
				kind := "auxiliary"
				if f.activeHeaders[n] {
					kind = "header"
				}
				if f.activeFooters[n] {
					kind = "footer"
				}
				if strings.HasPrefix(n, "word/header") && !f.activeHeaders[n] || strings.HasPrefix(n, "word/footer") && !f.activeFooters[n] {
					kind = "orphan"
				}
				parts = append(parts, textPart{f.auxiliary[n], n, kind})
			}
		}
		if core := f.xmls["docProps/core.xml"]; core != nil {
			core.walk(func(n *xmlNode) {
				if n.name == "creator" || n.name == "lastModifiedBy" {
					if strings.TrimSpace(n.text) != "" {
						parts = append(parts, textPart{n.text, "文件属性 / " + n.name, "metadata"})
					}
				}
			})
		}
	} else {
		page := 0
		count := 0
		for _, line := range strings.Split(req.Bid.Text, "\n") {
			if m := pageMarkerRE.FindStringSubmatch(line); m != nil {
				page, _ = strconv.Atoi(m[1])
				continue
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			count++
			loc := fmt.Sprintf("提取文本第 %d 段", count)
			if page > 0 && req.Bid.VerifiedPages {
				loc = fmt.Sprintf("第 %d 页，提取文本第 %d 段", page, count)
			}
			parts = append(parts, textPart{line, loc, "body"})
		}
	}
	return parts
}
func checkIdentities(r *Report, req Request, f *docFacts, rule *bidformat.Rule) {
	keywords := []string{}
	seen := map[string]bool{}
	for _, k := range req.IdentityKeywords {
		k = strings.TrimSpace(k)
		if k != "" && !seen[strings.ToLower(k)] {
			seen[strings.ToLower(k)] = true
			keywords = append(keywords, k)
		}
	}
	parts := textParts(req, f)
	matched := 0
	for _, part := range parts {
		for _, k := range keywords {
			if strings.Contains(strings.ToLower(part.text), strings.ToLower(k)) {
				c := Check{Category: "identity", Title: "身份关键词命中", Status: "review", Message: fmt.Sprintf("检出身份关键词“%s”，需结合招标条款确认是否应移除。", k), Location: part.location, Excerpt: matchExcerpt(part.text, k), Suggestion: "确认关键词是否代表投标方身份，并删除或按招标要求匿名化。"}
				if rule != nil && identityRequirementApplies(rule.Quote, part.kind, k) {
					c.Requirement = rule.Quote
					c.SourceFile = rule.Source.Name
					c.SourcePage = rule.Source.Page
					c.Status = "fail"
					c.Message = fmt.Sprintf("标书中检出“%s”，与招标文件的明确匿名要求不符。", k)
				}
				r.add(c)
				matched++
				if matched >= 100 {
					r.add(Check{Category: "identity", Title: "更多身份命中", Status: "review", Message: "身份命中超过 100 项，报告仅展示前 100 项。", Suggestion: "在原文件中搜索全部身份关键词。"})
					return
				}
				break
			}
		}
	}
	if len(keywords) > 0 && matched == 0 {
		c := Check{Category: "identity", Title: "身份关键词检索", Status: "pass", Message: "可读取文字中未命中提供的身份关键词；不代表图片或其他身份线索已通过。"}
		if rule != nil {
			c.Requirement = rule.Quote
			c.SourceFile = rule.Source.Name
			c.SourcePage = rule.Source.Page
		}
		r.add(c)
	} else if len(keywords) == 0 {
		r.add(Check{Category: "identity", Title: "补充投标方身份关键词", Status: "review", Message: "未提供企业全称、简称、姓名或联系方式等身份关键词，无法可靠核对实际投标方身份。", Suggestion: "补充本次投标方的真实身份关键词后重新检查。"})
	}
	// Generic patterns are candidates, never proof of the actual bidder. Project
	// owners, cited manufacturers and unrelated entities can be legitimate.
	count := 0
	for _, part := range parts {
		if hits := identityCandidateRE.FindAllString(part.text, -1); len(hits) > 0 {
			r.add(Check{Category: "identity", Title: "疑似身份信息需复核", Status: "review", Message: "检测到企业名称、邮箱或联系信息模式，需确认是否属于应隐藏的投标方身份。", Location: part.location, Excerpt: matchExcerpt(part.text, hits[0]), Suggestion: "对照匿名条款确认信息归属，避免误删项目业主或合法产品资料。"})
			count++
			if count >= 20 {
				break
			}
		}
	}
}

func identityRequirementApplies(quote, kind, keyword string) bool {
	if kind == "orphan" || kind == "auxiliary" {
		return false
	}
	if kind == "metadata" {
		return strings.Contains(quote, "文件属性") || strings.Contains(quote, "作者信息")
	}
	if strings.Contains(quote, "正文") {
		if kind != "body" && kind != "table" {
			return false
		}
	} else if strings.Contains(quote, "页眉") || strings.Contains(quote, "页脚") {
		if !(kind == "header" && strings.Contains(quote, "页眉")) && !(kind == "footer" && strings.Contains(quote, "页脚")) {
			return false
		}
	} else if strings.Contains(quote, "封面") || strings.Contains(quote, "封底") || strings.Contains(quote, "目录") || strings.Contains(quote, "签字") || strings.Contains(quote, "章节") {
		// These positions are not reliably classified from XML paragraphs.
		return false
	}
	// A name prohibition alone does not establish that phone numbers or email
	// addresses are forbidden. Their matches remain review candidates.
	if strings.Contains(keyword, "@") || isContactNumber(keyword) {
		return strings.Contains(quote, "联系方式") || strings.Contains(quote, "电话") || strings.Contains(quote, "邮箱") || strings.Contains(quote, "识别") || strings.Contains(quote, "身份")
	}
	return true
}
func isContactNumber(s string) bool {
	digits := 0
	for _, r := range s {
		if unicode.IsDigit(r) {
			digits++
		} else if r != '+' && r != '-' && r != ' ' && r != '(' && r != ')' {
			return false
		}
	}
	return digits >= 7
}

func checkPackage(r *Report, f *docFacts, rule *bidformat.Rule) {
	if f.images > 0 {
		c := Check{Category: "visual", Title: "图片与图形身份复核", Status: "review", Message: fmt.Sprintf("检测到 %d 个图片或图形对象，未识别其中的 Logo、印章、证书、二维码和文字。", f.images), Suggestion: "逐张核对图片中的身份线索和招标要求。"}
		if rule != nil {
			c.Requirement = rule.Quote
			c.SourceFile = rule.Source.Name
			c.SourcePage = rule.Source.Page
		}
		r.add(c)
	}
	if f.unsupported || f.missingParts {
		r.add(Check{Category: "document", Title: "未完整解析的文档对象", Status: "review", Message: "存在未解析的表格条件样式、嵌入对象、符号或缺失的引用内容，自动检查覆盖范围有限。", Suggestion: "在 Word 中核对相关对象实际显示效果和身份信息。"})
	}
	for _, item := range []struct {
		count                      int
		title, message, suggestion string
	}{{f.comments, "批注", "文件中存在批注，可能保留姓名或审核信息。", "清除批注，并核对其作者和内容是否泄露身份。"}, {f.revisions, "修订记录", "文件中存在插入、删除或格式修订记录。", "审阅并接受或拒绝全部修订，再检查最终文件。"}, {f.hidden, "隐藏文字", "文件中存在隐藏文字或仅网页隐藏文字。", "显示并核对隐藏文字，删除不应提交的内容。"}, {f.external, "外部链接", "文件包含外部链接或外部资源引用。", "核对链接地址、资源和邮箱是否泄露身份或造成提交后显示异常。"}} {
		if item.count > 0 {
			r.add(Check{Category: "document", Title: item.title + "需复核", Status: "review", Message: fmt.Sprintf("%s 检测数量：%d。", item.message, item.count), Suggestion: item.suggestion})
		}
	}
	if core := f.xmls["docProps/core.xml"]; core != nil {
		var metadata []string
		core.walk(func(n *xmlNode) {
			if (n.name == "creator" || n.name == "lastModifiedBy") && strings.TrimSpace(n.text) != "" {
				metadata = append(metadata, n.name+": "+n.text)
			}
		})
		if len(metadata) > 0 {
			r.add(Check{Category: "document", Title: "文件作者信息需复核", Status: "review", Message: "文件属性保留作者或最后修改者信息，需判断是否暴露投标人身份。", Location: "docProps/core.xml", Excerpt: short(strings.Join(metadata, "；")), Suggestion: "清除或按招标文件要求处理作者属性，并再次检查。"})
		}
	}
}
