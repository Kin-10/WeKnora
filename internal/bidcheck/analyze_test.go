package bidcheck

import (
	"archive/zip"
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/bidformat"
)

const wordNS = `http://schemas.openxmlformats.org/wordprocessingml/2006/main`

func packageDOCX(t *testing.T, body string, extra map[string]string) []byte {
	t.Helper()
	files := map[string]string{"word/document.xml": `<w:document xmlns:w="` + wordNS + `" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><w:body>` + body + `</w:body></w:document>`}
	for k, v := range extra {
		files[k] = v
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, e := z.Create(n)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write([]byte(files[n])); e != nil {
			t.Fatal(e)
		}
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func paragraph(text, rpr string) string {
	return `<w:p><w:r><w:rPr>` + rpr + `</w:rPr><w:t>` + text + `</w:t></w:r></w:p>`
}
func requestFor(tender string, data []byte) Request {
	return Request{Tender: Document{Name: "招标文件.txt", Format: "txt", Text: tender}, Bid: Document{Name: "技术标.docx", Format: "docx", Data: data}, Scope: bidformat.ScopeTechnical}
}
func checkWithTitle(t *testing.T, r *Report, title string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Title == title {
			return c
		}
	}
	t.Fatalf("missing check %q: %+v", title, r.Checks)
	return Check{}
}
func analyzeOK(t *testing.T, req Request) *Report {
	t.Helper()
	r, e := Analyze(req)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestExplicitFormattingFailureRetainsEvidence(t *testing.T) {
	tender := "技术标（暗标）制作要求：正文字体为宋体，四号。"
	r := analyzeOK(t, requestFor(tender, packageDOCX(t, paragraph("建设技术方案", `<w:rFonts w:eastAsia="黑体"/><w:sz w:val="28"/>`), nil)))
	c := checkWithTitle(t, r, "正文字体")
	if c.Status != "fail" || c.Requirement != tender || c.SourceFile != "招标文件.txt" || !strings.Contains(c.Location, "第 1 段") || !strings.Contains(c.Excerpt, "建设技术方案") {
		t.Fatalf("unusable failure evidence: %+v", c)
	}
	if checkWithTitle(t, r, "正文字号").Status != "pass" || r.Status != "fail" {
		t.Fatalf("unexpected report %+v", r)
	}
}

func TestStyleInheritanceAndUnknownFont(t *testing.T) {
	styles := `<w:styles xmlns:w="` + wordNS + `"><w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:eastAsia="宋体"/><w:sz w:val="28"/></w:rPr></w:rPrDefault></w:docDefaults><w:style w:type="paragraph" w:styleId="Normal" w:default="1"><w:name w:val="Normal"/></w:style><w:style w:type="paragraph" w:styleId="Custom"><w:basedOn w:val="Normal"/></w:style></w:styles>`
	body := `<w:p><w:pPr><w:pStyle w:val="Custom"/></w:pPr><w:r><w:t>技术方案</w:t></w:r></w:p>`
	req := requestFor("技术标（暗标）制作要求：正文字体为宋体，四号。", packageDOCX(t, body, map[string]string{"word/styles.xml": styles}))
	r := analyzeOK(t, req)
	if checkWithTitle(t, r, "正文字体").Status != "pass" || checkWithTitle(t, r, "正文字号").Status != "pass" || r.Status != "review" {
		t.Fatalf("inherited formatting %+v", r)
	}
	req.Bid.Data = packageDOCX(t, paragraph("技术方案", ""), nil)
	r = analyzeOK(t, req)
	if checkWithTitle(t, r, "正文字体").Status != "review" {
		t.Fatalf("unspecified font invented: %+v", r)
	}
}

func TestAnonymityNeedsExplicitApplicableRule(t *testing.T) {
	for _, tc := range []struct {
		name, tender string
		scope        bidformat.Scope
		status       string
	}{{"explicit", "技术标（暗标）不得出现供应商名称。", bidformat.ScopeTechnical, "fail"}, {"mention only", "技术标采用暗标评审。", bidformat.ScopeTechnical, "review"}, {"different clause", "投标人名称允许在封面列示。正文不得出现页眉。", bidformat.ScopeTechnical, "review"}, {"exception", "技术标（暗标）不得出现供应商名称，但封面允许显示供应商名称。", bidformat.ScopeTechnical, "review"}, {"wrong object", "技术标（暗标）不得出现供应商联系方式，但允许出现供应商名称。", bidformat.ScopeTechnical, "review"}, {"business volume", "技术标（暗标）不得出现供应商名称。", bidformat.ScopeBusiness, "review"}} {
		t.Run(tc.name, func(t *testing.T) {
			req := requestFor(tc.tender, packageDOCX(t, paragraph(strings.Repeat("技术说明", 80)+"天华有限公司负责实施。", ""), nil))
			req.Scope = tc.scope
			req.IdentityKeywords = []string{"天华有限公司"}
			r := analyzeOK(t, req)
			c := checkWithTitle(t, r, "身份关键词命中")
			if c.Status != tc.status || !strings.Contains(c.Excerpt, "天华有限公司") {
				t.Fatalf("identity false decision/evidence %+v", c)
			}
			if tc.status != "fail" && r.Summary.Failed != 0 {
				t.Fatalf("unsupported failure %+v", r)
			}
		})
	}
}

func TestConflictingRulesAreReview(t *testing.T) {
	r := analyzeOK(t, requestFor("技术标（暗标）制作要求：正文字体为宋体，四号。正文字体为黑体，三号。", packageDOCX(t, paragraph("技术方案", `<w:rFonts w:eastAsia="宋体"/><w:sz w:val="28"/>`), nil)))
	if r.Status != "review" || r.Summary.Failed != 0 {
		t.Fatalf("conflict wrongly resolved: %+v", r)
	}
	found := false
	for _, c := range r.Checks {
		if c.Title == "招标规则需复核" && strings.Contains(c.Requirement, "宋体") && strings.Contains(c.Requirement, "黑体") {
			found = true
		}
	}
	if !found {
		t.Fatalf("lost conflict provenance %+v", r)
	}
}

func TestTrackedHistoricalSectionsAndOrphanHeaderDoNotFail(t *testing.T) {
	body := paragraph("技术方案", "") + `<w:sectPr><w:pgMar w:top="1701" w:bottom="1701" w:left="1701" w:right="1701"/><w:sectPrChange><w:sectPr><w:pgMar w:top="1134" w:bottom="1134" w:left="1134" w:right="1134"/><w:headerReference r:id="old"/></w:sectPr></w:sectPrChange></w:sectPr>`
	extra := map[string]string{"word/header1.xml": `<w:hdr xmlns:w="` + wordNS + `"><w:p><w:r><w:t>已撤销页眉</w:t></w:r></w:p></w:hdr>`, "word/_rels/document.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="old" Target="header1.xml"/></Relationships>`}
	r := analyzeOK(t, requestFor("技术标（暗标）制作要求：上下左右边距均为3厘米。不得设置页眉。", packageDOCX(t, body, extra)))
	if r.Summary.Failed != 0 || checkWithTitle(t, r, "页面上边距").Status != "pass" || checkWithTitle(t, r, "页面页眉").Status != "pass" {
		t.Fatalf("historical formats treated as active: %+v", r)
	}
}

func TestThemeFillAndUnknownTableStyleCannotPass(t *testing.T) {
	body := `<w:tbl><w:tblPr><w:tblStyle w:val="ConditionalTable"/></w:tblPr><w:tr><w:tc><w:tcPr><w:shd w:val="clear" w:fill="auto" w:themeFill="accent1"/></w:tcPr>` + paragraph("表格技术参数", "") + `</w:tc></w:tr></w:tbl>`
	r := analyzeOK(t, requestFor("技术标（暗标）制作要求：字体不得有底色。所有文字不得加粗。", packageDOCX(t, body, nil)))
	if checkWithTitle(t, r, "全篇文字底纹").Status != "review" || checkWithTitle(t, r, "全篇文字加粗").Status != "review" {
		t.Fatalf("unknown style passed %+v", r)
	}
}

func TestFormattingToggleCascade(t *testing.T) {
	styles := `<w:styles xmlns:w="` + wordNS + `"><w:style w:type="paragraph" w:styleId="Normal" w:default="1"><w:rPr><w:b/></w:rPr></w:style><w:style w:type="character" w:styleId="Toggle"><w:rPr><w:b/></w:rPr></w:style></w:styles>`
	body := paragraph("技术方案", `<w:rStyle w:val="Toggle"/>`)
	r := analyzeOK(t, requestFor("技术标（暗标）所有文字不得加粗。", packageDOCX(t, body, map[string]string{"word/styles.xml": styles})))
	if checkWithTitle(t, r, "全篇文字加粗").Status != "pass" {
		t.Fatalf("style toggle ignored %+v", r)
	}
}

func TestNonDOCXAndAbsentRulesNeverPass(t *testing.T) {
	for _, format := range []string{"pdf", "doc", "txt"} {
		r := analyzeOK(t, Request{Tender: Document{Name: "招标文件.txt", Format: "txt", Text: "采购项目说明，无明确暗标编制条款。"}, Bid: Document{Name: "标书." + format, Format: format, Text: "技术方案"}})
		if r.Status != "review" || len(r.Limitations) == 0 || checkWithTitle(t, r, "原始格式与图片需复核").Status != "review" {
			t.Fatalf("unknown coverage passed %+v", r)
		}
	}
}

func TestPhysicalPagesRequireVerifiedMapping(t *testing.T) {
	for _, verified := range []bool{false, true} {
		req := Request{Tender: Document{Name: "招标.pdf", Format: "pdf", Text: "---PHYSICAL PAGE 7---\n技术标（暗标）不得出现供应商名称。", VerifiedPages: verified}, Bid: Document{Name: "标书.pdf", Format: "pdf", Text: "---PHYSICAL PAGE 2---\n天华有限公司", VerifiedPages: verified}, IdentityKeywords: []string{"天华有限公司"}}
		r := analyzeOK(t, req)
		c := checkWithTitle(t, r, "身份关键词命中")
		if verified {
			if c.SourcePage != 7 || !strings.Contains(c.Location, "第 2 页") {
				t.Fatalf("trusted page lost %+v", c)
			}
		} else {
			if c.SourcePage != 0 || strings.Contains(c.Location, "页") {
				t.Fatalf("unverified page promoted %+v", c)
			}
		}
	}
	r := analyzeOK(t, Request{Tender: Document{Name: "招标.txt", Format: "txt", Text: "56/123\n技术标（暗标）正文字体为宋体，四号。"}, Bid: Document{Name: "标书.txt", Format: "txt", Text: "技术方案"}})
	for _, rule := range r.Rules {
		if rule.SourcePage != 0 {
			t.Fatalf("printed label treated as physical page %+v", rule)
		}
	}
}

func TestExtractSkipsDeletedTenderClauses(t *testing.T) {
	body := `<w:p><w:del><w:r><w:delText>正文字体为黑体，三号。</w:delText></w:r></w:del><w:r><w:t>正文字体为宋体，四号。</w:t></w:r></w:p>`
	text, e := ExtractDOCXText(packageDOCX(t, body, nil))
	if e != nil || strings.Contains(text, "黑体") || !strings.Contains(text, "宋体") {
		t.Fatalf("deleted requirement retained %q %v", text, e)
	}
}

func TestZIPAndXMLSafetyLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{{"invalid", []byte("not zip")}, {"oversized XML", packageDOCX(t, paragraph(strings.Repeat("a", maxXMLBytes), ""), nil)}, {"deep XML", packageDOCX(t, strings.Repeat("<w:custom>", 129)+strings.Repeat("</w:custom>", 129), nil)}, {"unsafe path", packageDOCX(t, paragraph("技术方案", ""), map[string]string{"../escape.xml": "<x/>"})}, {"DTD", packageDOCX(t, paragraph("技术方案", ""), map[string]string{"word/comments.xml": `<!DOCTYPE x [<!ENTITY y "x">]><x/>`})}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := ExtractDOCXText(tc.data); e == nil {
				t.Fatal("unsafe package accepted")
			}
		})
	}
}

func TestSeparateIdentityExceptionRemainsReview(t *testing.T) {
	req := requestFor("技术标（暗标）不得出现供应商名称。封面允许出现供应商名称。", packageDOCX(t, paragraph("天华有限公司", ""), nil))
	req.IdentityKeywords = []string{"天华有限公司"}
	r := analyzeOK(t, req)
	if checkWithTitle(t, r, "身份关键词命中").Status != "review" || r.Summary.Failed != 0 {
		t.Fatalf("separate exception ignored %+v", r)
	}
}

func TestBodyProhibitionDoesNotFailMetadataAndContacts(t *testing.T) {
	extra := map[string]string{"docProps/core.xml": `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:creator>天华有限公司</dc:creator></cp:coreProperties>`}
	req := requestFor("技术标（暗标）正文不得出现供应商名称。", packageDOCX(t, paragraph("技术方案联系 user@example.com", ""), extra))
	req.IdentityKeywords = []string{"天华有限公司", "user@example.com"}
	r := analyzeOK(t, req)
	for _, c := range r.Checks {
		if c.Title == "身份关键词命中" && c.Status != "review" {
			t.Fatalf("local name requirement applied to unrelated location/object %+v", c)
		}
	}
}

func TestXMLNodeBudgetAppliesAcrossPackage(t *testing.T) {
	large := "<x>" + strings.Repeat("<a/>", maxXMLNodes/2) + "</x>"
	data := packageDOCX(t, paragraph("技术方案", ""), map[string]string{"word/extra1.xml": large, "word/extra2.xml": large})
	if _, e := ExtractDOCXText(data); e == nil {
		t.Fatal("per-entry node limits bypassed global XML budget")
	}
}

func TestIdentityExcerptPreservesUnicodeOffset(t *testing.T) {
	text := strings.Repeat("İK", 100) + "公司AAA"
	excerpt := matchExcerpt(text, "aaa")
	if !strings.Contains(excerpt, "AAA") || strings.ContainsRune(excerpt, '\uFFFD') {
		t.Fatalf("broken match excerpt %q", excerpt)
	}
}

func TestLocalIdentityProhibitionDoesNotApplyToOtherPositions(t *testing.T) {
	for _, clause := range []string{"页眉不得出现投标人名称。", "封面不得出现投标人名称。", "目录不得出现投标人名称。"} {
		req := requestFor("技术标（暗标）"+clause, packageDOCX(t, paragraph("天华有限公司", ""), nil))
		req.IdentityKeywords = []string{"天华有限公司"}
		r := analyzeOK(t, req)
		if checkWithTitle(t, r, "身份关键词命中").Status != "review" {
			t.Fatalf("local prohibition applied to body %+v", r)
		}
	}
}
