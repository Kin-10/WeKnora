package bidformat

import (
	"fmt"
	"sort"
	"strconv"
)

// Resolve combines general and selected-volume rules. A specific rule is not
// silently preferred over a conflicting general one: the tender needs review.
// Conflicting properties remain absent from Spec and are reported as issues.
func (r Result) Resolve(scope Scope) (Spec, []Issue) {
	spec := Spec{Scope: scope}
	issues := make([]Issue, 0)
	if scope != ScopeAll && scope != ScopeBusiness && scope != ScopeTechnical {
		return spec, []Issue{{Code: "unsupported_scope", Scope: scope, Message: "未识别的标书分册类型"}}
	}
	for _, issue := range r.Issues {
		if issue.Scope == "" || issue.Scope == ScopeAll || issue.Scope == scope {
			issues = append(issues, issue)
		}
	}
	byKey := make(map[string][]Rule)
	for _, rule := range r.Rules {
		if rule.Scope != ScopeAll && rule.Scope != scope {
			continue
		}
		key := rule.Target + "." + rule.Property
		byKey[key] = append(byKey[key], rule)
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	blocked := make(map[string]bool)
	// Universal prohibitions also constrain headings and table runs. Treat a
	// contradictory local instruction as a conflict, not an implicit override.
	for _, key := range keys {
		rules := byKey[key]
		if rules[0].Target != "body" && rules[0].Target != "heading" && rules[0].Target != "table" {
			continue
		}
		allKey := "all_text." + rules[0].Property
		universal := byKey[allKey]
		if len(universal) == 0 || rulesConflict(universal) {
			continue
		}
		for _, rule := range rules {
			if rule.Value != universal[0].Value {
				blocked[key], blocked[allKey] = true, true
				combined := append(append([]Rule{}, universal...), rules...)
				issues = append(issues, Issue{Code: "conflict", Scope: scope, Property: key, Message: "全篇格式要求与局部格式要求冲突，需确认后生成", Rules: combined})
				break
			}
		}
	}
	for _, key := range keys {
		if blocked[key] {
			continue
		}
		rules := byKey[key]
		if rulesConflict(rules) {
			issues = append(issues, Issue{Code: "conflict", Scope: scope, Property: key, Message: "招标文件对同一格式项存在不同要求，需确认后生成", Rules: rules})
			continue
		}
		if !applyRule(&spec, rules[0]) {
			issues = append(issues, Issue{Code: "unsupported", Scope: scope, Property: key, Message: "该格式项尚未支持自动应用", Rules: rules})
		} else {
			spec.Rules = append(spec.Rules, rules...)
		}
	}
	if len(spec.Rules) == 0 && len(issues) == 0 {
		issues = append(issues, Issue{Code: "missing", Scope: scope, Message: "本次招标文件中未找到该分册明确的格式要求"})
	}
	return spec, issues
}

func rulesConflict(rules []Rule) bool {
	for _, rule := range rules[1:] {
		if rule.Value != rules[0].Value {
			return true
		}
	}
	return false
}

func applyRule(spec *Spec, r Rule) bool {
	stringValue := r.Value
	intValue, intErr := strconv.Atoi(r.Value)
	boolValue, boolErr := strconv.ParseBool(r.Value)
	var style *TextStyle
	switch r.Target {
	case "body":
		style = &spec.Body
	case "heading":
		style = &spec.Heading
	case "table":
		style = &spec.Table
	case "all_text":
		style = &spec.AllText
	}
	if style != nil {
		switch r.Property {
		case "font_family":
			style.FontFamily = &stringValue
		case "font_size_half_points":
			if intErr != nil {
				return false
			}
			style.FontSizeHalfPoints = &intValue
		case "color":
			style.Color = &stringValue
		case "bold":
			if boolErr != nil {
				return false
			}
			style.Bold = &boolValue
		case "italic":
			if boolErr != nil {
				return false
			}
			style.Italic = &boolValue
		case "underline":
			if boolErr != nil {
				return false
			}
			style.Underline = &boolValue
		case "shading":
			if boolErr != nil || boolValue {
				return false
			}
			style.Shading = &boolValue
		case "character_spacing_twips":
			if intErr != nil {
				return false
			}
			style.CharacterSpacingTwips = &intValue
		case "position_half_points":
			if intErr != nil {
				return false
			}
			style.PositionHalfPoints = &intValue
		case "line_spacing_twips":
			if intErr != nil {
				return false
			}
			style.LineSpacingTwips = &intValue
		case "line_rule":
			style.LineRule = &stringValue
		case "alignment":
			style.Alignment = &stringValue
		case "first_line_chars":
			if intErr != nil {
				return false
			}
			style.FirstLineChars = &intValue
		case "space_before_twips":
			if intErr != nil {
				return false
			}
			style.SpaceBeforeTwips = &intValue
		case "space_after_twips":
			if intErr != nil {
				return false
			}
			style.SpaceAfterTwips = &intValue
		default:
			return false
		}
		return true
	}
	switch r.Target {
	case "page":
		switch r.Property {
		case "paper":
			spec.Page.Paper = &stringValue
		case "margin_top_twips":
			if intErr != nil {
				return false
			}
			spec.Page.MarginTopTwips = &intValue
		case "margin_bottom_twips":
			if intErr != nil {
				return false
			}
			spec.Page.MarginBottomTwips = &intValue
		case "margin_left_twips":
			if intErr != nil {
				return false
			}
			spec.Page.MarginLeftTwips = &intValue
		case "margin_right_twips":
			if intErr != nil {
				return false
			}
			spec.Page.MarginRightTwips = &intValue
		case "header":
			if boolErr != nil {
				return false
			}
			spec.Page.Header = &boolValue
		case "footer":
			if boolErr != nil {
				return false
			}
			spec.Page.Footer = &boolValue
		case "page_numbers":
			if boolErr != nil {
				return false
			}
			spec.Page.PageNumbers = &boolValue
		default:
			return false
		}
		return true
	case "structure":
		if boolErr != nil {
			return false
		}
		switch r.Property {
		case "cover":
			spec.Structure.Cover = &boolValue
		case "toc":
			spec.Structure.TOC = &boolValue
		case "back_cover":
			spec.Structure.BackCover = &boolValue
		case "blank_pages":
			spec.Structure.BlankPages = &boolValue
		default:
			return false
		}
		return true
	case "document":
		if r.Property == "anonymous" && boolErr == nil {
			spec.Anonymous = &boolValue
			return true
		}
	}
	return false
}

// Summary is deterministic and contains only explicitly resolved requirements.
// It is suitable for injecting into a drafting prompt alongside the evidence.
func (s Spec) Summary() string {
	result := ""
	for _, rule := range s.Rules {
		locator := rule.Source.Name
		if locator == "" {
			locator = rule.Source.ID
		}
		if rule.Source.Page > 0 {
			locator += fmt.Sprintf("，第%d页", rule.Source.Page)
		} else if rule.Source.ChunkID != "" {
			locator += "，片段" + rule.Source.ChunkID
		}
		result += fmt.Sprintf("%s.%s=%s（来源：%s，原文：%s）\n", rule.Target, rule.Property, rule.Value, locator, rule.Quote)
	}
	return result
}
