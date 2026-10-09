package bidformat

import (
	"regexp"
	"strings"
)

// Structure is the deterministic decomposition of a tender text into drafting
// scopes: separated volumes (商务标/技术标) and lots (包段). It powers the
// scope-selection card at task start, so the picker appears the moment the
// uploaded tender finishes parsing instead of one planning round later.
type TenderStructure struct {
	// Lots are the detected bidding lots in first-appearance order. Empty when
	// the tender is single-lot (or the text is not tender-like).
	Lots []TenderLot
	// SeparateVolumes marks tenders whose commercial and technical parts must
	// be drafted as two volumes, never one combined document.
	SeparateVolumes bool
	// AnonymousTechnical marks a technical volume that must not carry any
	// supplier identity (暗标).
	AnonymousTechnical bool
}

// Lot is one bidding lot extracted verbatim from the tender text.
type TenderLot struct {
	// Label is the canonical lot marker, e.g. "第1包".
	Label string
	// Detail is the same-line description after the marker, capped in length.
	Detail string
}

// HasChoice reports whether the structure gives the user a real scope choice.
func (s TenderStructure) HasChoice() bool {
	return s.SeparateVolumes || len(s.Lots) > 1
}

var (
	lotMarkerRE  = regexp.MustCompile(`第\s*([0-9一二三四五六七八九十百零]+)\s*包|标段\s*([0-9一二三四五六七八九十百零]+)`)
	lotCountRE   = regexp.MustCompile(`分\s*为\s*([0-9一二三四五六七八九十百零]+)\s*个\s*包|共\s*([0-9一二三四五六七八九十百零]+)\s*个\s*包`)
	tenderishRE  = regexp.MustCompile(`招标|采购|询价|竞标|磋商|谈判|比选`)
	commercialRE = regexp.MustCompile(`商务标|商务部分|商务册`)
	technicalRE  = regexp.MustCompile(`技术标|技术部分|技术暗标|技术文件`)
	separateRE   = regexp.MustCompile(`分开|分别|分册|单独|各自|两册|另行|独立编制`)
	anonymousRE  = regexp.MustCompile(`技术暗标|技术标（暗标）|技术标\(暗标\)|暗标`)

	chineseDigits = map[rune]int{'零': 0, '一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
)

// DetectStructure extracts lots and volume separation from raw tender text.
// It is deliberately regex-based: no LLM, no retrieval, safe to run on every
// parsed attachment. False positives only shape a user-confirmed picker, so
// detection errs on the side of asking.
func DetectStructure(text string) TenderStructure {
	var structure TenderStructure
	if !tenderishRE.MatchString(text) {
		return structure
	}
	structure.Lots = detectLots(text)
	structure.AnonymousTechnical = anonymousRE.MatchString(text) && technicalRE.MatchString(text)
	structure.SeparateVolumes = commercialRE.MatchString(text) && technicalRE.MatchString(text) &&
		(separateRE.MatchString(text) || structure.AnonymousTechnical)
	return structure
}

func detectLots(text string) []TenderLot {
	seen := map[int]bool{}
	lots := make([]TenderLot, 0, 8)
	for _, line := range strings.Split(text, "\n") {
		for _, match := range lotMarkerRE.FindAllStringSubmatch(line, -1) {
			number := match[1]
			if number == "" {
				number = match[2]
			}
			value, ok := parseChineseOrDigitNumber(number)
			if !ok || value <= 0 || value > 99 || seen[value] {
				continue
			}
			seen[value] = true
			lots = append(lots, TenderLot{Label: "第" + number + "包", Detail: lotDetail(line, match[0])})
			if len(lots) >= 12 {
				return lots
			}
		}
	}
	if len(lots) > 0 {
		return lots
	}
	// No explicit 第X包 markers; a stated total count still yields a picker.
	if match := lotCountRE.FindStringSubmatch(text); match != nil {
		raw := match[1]
		if raw == "" {
			raw = match[2]
		}
		if value, ok := parseChineseOrDigitNumber(raw); ok && value >= 2 && value <= 12 {
			for i := 1; i <= value; i++ {
				lots = append(lots, TenderLot{Label: "第" + parseDigitString(i) + "包"})
			}
		}
	}
	return lots
}

func lotDetail(line, marker string) string {
	index := strings.Index(line, marker)
	if index < 0 {
		return ""
	}
	detail := strings.TrimSpace(line[index+len(marker):])
	detail = strings.TrimPrefix(detail, "：")
	detail = strings.TrimPrefix(detail, ":")
	detail = strings.TrimSpace(detail)
	runes := []rune(detail)
	if len(runes) > 80 {
		runes = runes[:80]
	}
	return string(runes)
}

func parseChineseOrDigitNumber(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if value := parseDigitStringToInt(raw); value >= 0 {
		return value, true
	}
	// Simple Chinese numerals: 十, N十, 十M, N十M — plenty for lot counts.
	runes := []rune(raw)
	if len(runes) == 1 {
		if runes[0] == '十' {
			return 10, true
		}
		if value, ok := chineseDigits[runes[0]]; ok {
			return value, true
		}
		return 0, false
	}
	total, ok := 0, false
	for _, r := range runes {
		digit, known := chineseDigits[r]
		if !known {
			return 0, false
		}
		if r == '十' {
			if total == 0 {
				total = 10
			} else {
				total *= 10
			}
			ok = true
			continue
		}
		total += digit
		ok = true
	}
	if !ok {
		return 0, false
	}
	return total, true
}

func parseDigitStringToInt(raw string) int {
	value := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return -1
		}
		value = value*10 + int(r-'0')
	}
	return value
}

func parseDigitString(value int) string {
	if value < 10 {
		return string(rune('0' + value))
	}
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
