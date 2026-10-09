package bidcheck

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	// MaxFileBytes is the source-file cap shared with the upload handler.
	MaxFileBytes     = 200 << 20
	maxArchiveBytes  = MaxFileBytes
	maxXMLBytes      = 8 << 20
	maxExpandedBytes = 512 << 20
	maxZIPEntries    = 1000
	maxXMLNodes      = 200000
)

type xmlNode struct {
	name      string
	namespace string
	attrs     map[string]string
	children  []*xmlNode
	text      string
	textParts []string
}

func (n *xmlNode) child(name string) *xmlNode {
	if n == nil {
		return nil
	}
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

func (n *xmlNode) attr(name string) string {
	if n == nil {
		return ""
	}
	return n.attrs[name]
}

func (n *xmlNode) walk(fn func(*xmlNode)) {
	if n == nil {
		return
	}
	fn(n)
	for _, c := range n.children {
		c.walk(fn)
	}
}

func (n *xmlNode) walkActive(fn func(*xmlNode)) {
	if n == nil || n.name == "del" || n.name == "moveFrom" || strings.HasSuffix(n.name, "Change") {
		return
	}
	fn(n)
	for _, c := range n.children {
		c.walkActive(fn)
	}
}

func parseXML(data []byte, nodeBudget, tokenBudget *int) (*xmlNode, error) {
	if len(data) > maxXMLBytes {
		return nil, fmt.Errorf("DOCX XML 超出安全大小限制")
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	var stack []*xmlNode
	var root *xmlNode
	count := 0
	for {
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("DOCX XML 无法解析: %w", err)
		}
		*tokenBudget--
		if *tokenBudget < 0 {
			return nil, fmt.Errorf("DOCX XML token 总量超出安全限制")
		}
		switch v := t.(type) {
		case xml.StartElement:
			count++
			*nodeBudget--
			if count > maxXMLNodes || *nodeBudget < 0 || len(stack) >= 128 {
				return nil, fmt.Errorf("DOCX XML 结构超出安全限制")
			}
			n := &xmlNode{name: v.Name.Local, namespace: v.Name.Space, attrs: map[string]string{}}
			for _, a := range v.Attr {
				n.attrs[a.Name.Local] = a.Value
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, fmt.Errorf("DOCX XML 存在多个根节点")
				}
				root = n
			} else {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("DOCX XML 结构无效")
			}
			n := stack[len(stack)-1]
			n.text = strings.Join(n.textParts, "")
			n.textParts = nil
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				n := stack[len(stack)-1]
				n.textParts = append(n.textParts, string(v))
			}
		case xml.Directive:
			return nil, fmt.Errorf("DOCX XML 不允许文档类型或实体声明")
		}
	}
	if root == nil || len(stack) != 0 {
		return nil, fmt.Errorf("DOCX XML 内容为空或不完整")
	}
	return root, nil
}

// readDOCX never extracts files to disk or follows package/external links. It
// bounds both claimed and actually expanded sizes, including binary media.
func readDOCX(data []byte) (map[string]*xmlNode, error) {
	if len(data) == 0 || len(data) > maxArchiveBytes {
		return nil, fmt.Errorf("DOCX 文件为空或超过 200 MiB")
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("不是有效的 DOCX 文件: %w", err)
	}
	if len(z.File) > maxZIPEntries {
		return nil, fmt.Errorf("DOCX 文件条目过多")
	}
	var total uint64
	seen := map[string]bool{}
	xmls := map[string]*xmlNode{}
	nodeBudget := maxXMLNodes
	tokenBudget := maxXMLNodes * 8
	for _, f := range z.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		if seen[f.Name] || path.Clean(f.Name) != f.Name || strings.HasPrefix(f.Name, "/") || strings.Contains(f.Name, "\\") || strings.HasPrefix(f.Name, "../") {
			return nil, fmt.Errorf("DOCX 文件路径或重复条目无效")
		}
		seen[f.Name] = true
		if f.UncompressedSize64 > maxExpandedBytes || total > maxExpandedBytes-f.UncompressedSize64 {
			return nil, fmt.Errorf("DOCX 解压大小超出安全限制")
		}
		total += f.UncompressedSize64
		if !strings.HasSuffix(strings.ToLower(f.Name), ".xml") && !strings.HasSuffix(strings.ToLower(f.Name), ".rels") {
			continue
		}
		if f.UncompressedSize64 > maxXMLBytes {
			return nil, fmt.Errorf("DOCX XML 超出安全大小限制")
		}
		r, e := f.Open()
		if e != nil {
			return nil, fmt.Errorf("无法读取 DOCX 条目: %w", e)
		}
		b, e := io.ReadAll(io.LimitReader(r, maxXMLBytes+1))
		closeErr := r.Close()
		if e != nil {
			return nil, fmt.Errorf("无法解压 DOCX XML: %w", e)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(b) > maxXMLBytes {
			return nil, fmt.Errorf("DOCX XML 超出安全大小限制")
		}
		n, e := parseXML(b, &nodeBudget, &tokenBudget)
		if e != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, e)
		}
		xmls[f.Name] = n
	}
	if xmls["word/document.xml"] == nil || xmls["word/document.xml"].name != "document" || xmls["word/document.xml"].child("body") == nil {
		return nil, fmt.Errorf("DOCX 缺少有效正文")
	}
	for _, name := range []string{"word/document.xml", "word/styles.xml"} {
		if n := xmls[name]; n != nil && n.namespace != "http://schemas.openxmlformats.org/wordprocessingml/2006/main" && n.namespace != "http://purl.oclc.org/ooxml/wordprocessingml/main" {
			return nil, fmt.Errorf("DOCX Word XML 命名空间无效")
		}
	}
	return xmls, nil
}

func nodeText(n *xmlNode) string {
	var b strings.Builder
	n.walk(func(c *xmlNode) {
		switch c.name {
		case "t", "delText":
			b.WriteString(c.text)
		case "tab":
			b.WriteByte('\t')
		case "br", "cr":
			b.WriteByte('\n')
		}
	})
	return b.String()
}

func activeNodeText(n *xmlNode) string {
	var b strings.Builder
	var walk func(*xmlNode)
	walk = func(c *xmlNode) {
		if c == nil || c.name == "del" || c.name == "moveFrom" {
			return
		}
		switch c.name {
		case "t":
			b.WriteString(c.text)
		case "tab":
			b.WriteByte('\t')
		case "br", "cr":
			b.WriteByte('\n')
		}
		for _, child := range c.children {
			walk(child)
		}
	}
	walk(n)
	return b.String()
}

// ExtractDOCXText reads body paragraphs (including tables) with the same ZIP
// and XML limits as Analyze. It does not use a remote document parser.
func ExtractDOCXText(data []byte) (string, error) {
	xmls, e := readDOCX(data)
	if e != nil {
		return "", e
	}
	var lines []string
	var walk func(*xmlNode)
	walk = func(n *xmlNode) {
		if n.name == "del" || n.name == "moveFrom" {
			return
		}
		if n.name == "p" {
			if t := strings.TrimSpace(activeNodeText(n)); t != "" {
				lines = append(lines, t)
			}
			return
		}
		for _, c := range n.children {
			walk(c)
		}
	}
	walk(xmls["word/document.xml"].child("body"))
	return strings.Join(lines, "\n"), nil
}

type properties map[string]string

func copyProperties(a properties) properties {
	b := properties{}
	for k, v := range a {
		b[k] = v
	}
	return b
}
func mergeProperties(dst, src properties, toggle bool) {
	for k, v := range src {
		if toggle && (k == "bold" || k == "italic") {
			if v == "true" {
				if dst[k] == "true" {
					dst[k] = "false"
				} else {
					dst[k] = "true"
				}
			}
		} else {
			dst[k] = v
		}
	}
}
func boolAttr(n *xmlNode) string {
	v := strings.ToLower(n.attr("val"))
	if v == "false" || v == "0" || v == "off" {
		return "false"
	}
	return "true"
}
func runProperties(n *xmlNode) properties {
	p := properties{}
	if n == nil {
		return p
	}
	for _, c := range n.children {
		switch c.name {
		case "rFonts":
			if v := c.attr("eastAsia"); v != "" {
				p["font_east"] = v
			}
			if v := c.attr("ascii"); v != "" {
				p["font_ascii"] = v
			}
			if c.attr("eastAsiaTheme") != "" {
				p["font_east"] = "?"
			}
			if c.attr("asciiTheme") != "" {
				p["font_ascii"] = "?"
			}
		case "sz":
			p["font_size_half_points"] = c.attr("val")
		case "color":
			p["color"] = strings.ToUpper(c.attr("val"))
			if c.attr("themeColor") != "" || strings.EqualFold(c.attr("val"), "auto") {
				p["color"] = "?"
			}
		case "b":
			p["bold"] = boolAttr(c)
		case "i":
			p["italic"] = boolAttr(c)
		case "u":
			if c.attr("val") == "none" {
				p["underline"] = "false"
			} else {
				p["underline"] = "true"
			}
		case "shd":
			if c.attr("themeFill") != "" || c.attr("themeFillTint") != "" || c.attr("themeFillShade") != "" {
				p["shading"] = "?"
			} else if c.attr("val") == "nil" || (c.attr("fill") == "auto" && (c.attr("val") == "clear" || c.attr("val") == "")) {
				p["shading"] = "false"
			} else {
				p["shading"] = "true"
			}
		case "highlight":
			if c.attr("val") == "none" {
				p["shading"] = "false"
			} else {
				p["shading"] = "true"
			}
		case "spacing":
			p["character_spacing_twips"] = c.attr("val")
		case "position":
			p["position_half_points"] = c.attr("val")
		case "vanish", "webHidden":
			p["hidden"] = boolAttr(c)
		}
	}
	return p
}
func paragraphProperties(n *xmlNode) properties {
	p := properties{}
	if n == nil {
		return p
	}
	for _, c := range n.children {
		switch c.name {
		case "jc":
			p["alignment"] = c.attr("val")
		case "outlineLvl":
			p["outline"] = c.attr("val")
		case "spacing":
			for _, kv := range []struct{ a, k string }{{"line", "line_spacing_twips"}, {"lineRule", "line_rule"}, {"before", "space_before_twips"}, {"after", "space_after_twips"}} {
				if v := c.attr(kv.a); v != "" {
					p[kv.k] = v
				}
			}
			if c.attr("line") != "" && c.attr("lineRule") == "" {
				p["line_rule"] = "auto"
			}
			if c.attr("beforeLines") != "" || c.attr("beforeAutospacing") == "1" {
				p["space_before_twips"] = "?"
			}
			if c.attr("afterLines") != "" || c.attr("afterAutospacing") == "1" {
				p["space_after_twips"] = "?"
			}
		case "ind":
			if v := c.attr("firstLineChars"); v != "" {
				p["first_line_chars"] = v
			} else if v := c.attr("firstLine"); v != "" && v != "0" {
				p["first_line_chars"] = "?"
			}
			if c.attr("hanging") != "" || c.attr("hangingChars") != "" {
				p["first_line_chars"] = "?"
			}
		case "shd":
			mergeProperties(p, runProperties(&xmlNode{children: []*xmlNode{c}}), false)
		}
	}
	return p
}

type style struct {
	base    string
	props   properties
	heading bool
}
type docRun struct {
	text  string
	props properties
}
type docParagraph struct {
	text, location, target string
	props                  properties
	runs                   []docRun
}
type docFacts struct {
	xmls                                          map[string]*xmlNode
	paragraphs                                    []docParagraph
	sections                                      []properties
	auxiliary                                     map[string]string
	images, comments, revisions, hidden, external int
	toc                                           bool
	defaultProps                                  properties
	activeHeaders                                 map[string]bool
	activeFooters                                 map[string]bool
	missingParts                                  bool
	unsupported                                   bool
}

func inspectDOCX(data []byte) (*docFacts, error) {
	xmls, e := readDOCX(data)
	if e != nil {
		return nil, e
	}
	f := &docFacts{xmls: xmls, auxiliary: map[string]string{}, activeHeaders: map[string]bool{}, activeFooters: map[string]bool{}}
	defaults := properties{"bold": "false", "italic": "false", "underline": "false", "shading": "false", "hidden": "false", "character_spacing_twips": "0", "position_half_points": "0", "alignment": "left", "first_line_chars": "0", "space_before_twips": "0", "space_after_twips": "0"}
	styles := map[string]style{}
	normal := ""
	if s := xmls["word/styles.xml"]; s != nil {
		dd := s.child("docDefaults")
		mergeProperties(defaults, runProperties(dd.child("rPrDefault").child("rPr")), false)
		mergeProperties(defaults, paragraphProperties(dd.child("pPrDefault").child("pPr")), false)
		for _, n := range s.children {
			if n.name == "style" {
				pr := paragraphProperties(n.child("pPr"))
				mergeProperties(pr, runProperties(n.child("rPr")), false)
				name := strings.ToLower(n.child("name").attr("val"))
				id := n.attr("styleId")
				heading := strings.HasPrefix(name, "heading") || strings.Contains(name, "标题") || (pr["outline"] != "" && pr["outline"] != "9")
				if heading && pr["outline"] == "" {
					pr["outline"] = "0"
				}
				styles[id] = style{base: n.child("basedOn").attr("val"), props: pr, heading: heading}
				if n.attr("type") == "paragraph" && boolAttr(&xmlNode{attrs: map[string]string{"val": n.attr("default")}}) == "true" && n.attr("default") != "" {
					normal = id
				}
			}
		}
	}
	var applyStyle func(properties, string, map[string]bool) bool
	applyStyle = func(p properties, id string, seen map[string]bool) bool {
		if id == "" {
			return true
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		s, ok := styles[id]
		if !ok {
			return false
		}
		valid := applyStyle(p, s.base, seen)
		mergeProperties(p, s.props, true)
		return valid
	}
	f.defaultProps = defaults
	count := 0
	var walkBody func(*xmlNode, bool, string, bool)
	walkBody = func(n *xmlNode, inTable bool, tableShading string, unknownTableStyle bool) {
		if n.name == "del" || n.name == "moveFrom" {
			return
		}
		if n.name == "tbl" {
			inTable = true
			unknownTableStyle = n.child("tblPr").child("tblStyle").attr("val") != ""
			if unknownTableStyle {
				f.unsupported = true
			}
			if shd := n.child("tblPr").child("shd"); shd != nil {
				tableShading = runProperties(&xmlNode{children: []*xmlNode{shd}})["shading"]
			}
		}
		if n.name == "tc" {
			if shd := n.child("tcPr").child("shd"); shd != nil {
				tableShading = runProperties(&xmlNode{children: []*xmlNode{shd}})["shading"]
			}
		}
		if n.name == "p" {
			count++
			pp := n.child("pPr")
			id := pp.child("pStyle").attr("val")
			if id == "" {
				id = normal
			}
			p := copyProperties(defaults)
			if unknownTableStyle {
				p = properties{}
			}
			if !applyStyle(p, id, map[string]bool{}) {
				p = properties{}
				applyStyle(p, id, map[string]bool{})
			}
			mergeProperties(p, paragraphProperties(pp), false)
			target := "body"
			if inTable {
				target = "table"
			} else if (p["outline"] != "" && p["outline"] != "9") || styles[id].heading {
				target = "heading"
			}
			para := docParagraph{text: activeNodeText(n), location: fmt.Sprintf("正文第 %d 段", count), target: target, props: p}
			var walkRuns func(*xmlNode)
			walkRuns = func(c *xmlNode) {
				if c.name == "del" || c.name == "moveFrom" {
					return
				}
				if c.name == "r" {
					rp := copyProperties(p)
					rid := c.child("rPr").child("rStyle").attr("val")
					if rid != "" {
						if !applyStyle(rp, rid, map[string]bool{}) {
							rp = properties{}
							applyStyle(rp, rid, map[string]bool{})
						}
					}
					mergeProperties(rp, runProperties(c.child("rPr")), false)
					if tableShading == "true" || tableShading == "?" {
						rp["shading"] = tableShading
					}
					text := activeNodeText(c)
					if strings.TrimSpace(text) != "" {
						para.runs = append(para.runs, docRun{text: text, props: rp})
						if rp["hidden"] == "true" {
							f.hidden++
						}
					}
				}
				for _, child := range c.children {
					walkRuns(child)
				}
			}
			walkRuns(n)
			if strings.TrimSpace(para.text) != "" {
				f.paragraphs = append(f.paragraphs, para)
			}
			return
		}
		for _, c := range n.children {
			walkBody(c, inTable, tableShading, unknownTableStyle)
		}
	}
	walkBody(xmls["word/document.xml"].child("body"), false, "", false)
	relations := map[string]string{}
	if rels := xmls["word/_rels/document.xml.rels"]; rels != nil {
		for _, rel := range rels.children {
			if rel.name == "Relationship" && rel.attr("TargetMode") != "External" {
				relations[rel.attr("Id")] = path.Clean(path.Join("word", rel.attr("Target")))
			}
		}
	}
	xmls["word/document.xml"].child("body").walkActive(func(n *xmlNode) {
		if n.name == "headerReference" || n.name == "footerReference" {
			part := relations[n.attr("id")]
			if part == "" || xmls[part] == nil {
				f.missingParts = true
				return
			}
			if n.name == "headerReference" {
				f.activeHeaders[part] = true
			} else {
				f.activeFooters[part] = true
			}
		}
	})
	names := make([]string, 0, len(xmls))
	for name := range xmls {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		n := xmls[name]
		if strings.HasPrefix(name, "word/header") || strings.HasPrefix(name, "word/footer") || name == "word/comments.xml" || name == "word/footnotes.xml" || name == "word/endnotes.xml" {
			f.auxiliary[name] = nodeText(n)
		}
		n.walk(func(c *xmlNode) {
			switch c.name {
			case "drawing", "pict":
				f.images++
			case "comment":
				f.comments++
			case "ins", "del", "moveFrom", "moveTo", "rPrChange", "pPrChange":
				f.revisions++
			case "Relationship":
				if c.attr("TargetMode") == "External" {
					f.external++
				}
			case "altChunk", "object", "sym":
				f.unsupported = true
			}
		})
	}
	xmls["word/document.xml"].child("body").walkActive(func(c *xmlNode) {
		if c.name == "sectPr" {
			p := properties{}
			if m := c.child("pgMar"); m != nil {
				for _, side := range []string{"top", "bottom", "left", "right"} {
					p["margin_"+side+"_twips"] = m.attr(side)
				}
			}
			if s := c.child("pgSz"); s != nil {
				w, _ := strconv.Atoi(s.attr("w"))
				h, _ := strconv.Atoi(s.attr("h"))
				if abs(w-11906) <= 5 && abs(h-16838) <= 5 || abs(h-11906) <= 5 && abs(w-16838) <= 5 {
					p["paper"] = "A4"
				} else if w > 0 && h > 0 {
					p["paper"] = fmt.Sprintf("%dx%d twips", w, h)
				}
			}
			f.sections = append(f.sections, p)
		}
		if c.name == "instrText" && strings.Contains(strings.ToUpper(c.text), "TOC ") {
			f.toc = true
		}
		if c.name == "fldSimple" && strings.Contains(strings.ToUpper(c.attr("instr")), "TOC ") {
			f.toc = true
		}
		if c.name == "docPartGallery" && strings.Contains(strings.ToLower(c.attr("val")), "table of contents") {
			f.toc = true
		}
	})
	return f, nil
}
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
func hasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
