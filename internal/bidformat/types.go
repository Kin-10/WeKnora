// Package bidformat extracts explicitly stated bid document formatting rules
// with their provenance. It neither invents tender requirements nor chooses a
// template from another project.
package bidformat

type Scope string

const (
	ScopeAll       Scope = "all"
	ScopeBusiness  Scope = "business"
	ScopeTechnical Scope = "technical"
)

// Source is a caller-authorized tender document, page or chunk. Text must be
// the original extracted text, never an LLM summary. SHA256 is filled by Parse
// when absent. Page uses a one-based physical PDF page, when known.
type Source struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Text    string `json:"text"`
	SHA256  string `json:"sha256"`
	Page    int    `json:"page,omitempty"`
	ChunkID string `json:"chunk_id,omitempty"`
}

// Rule contains one supported explicit requirement and the original evidence.
// Target is body, heading, table, all_text, page or structure. Property and Value
// use the names/units of Spec. The source quote is never rewritten.
type Rule struct {
	Scope    Scope  `json:"scope"`
	Target   string `json:"target"`
	Property string `json:"property"`
	Value    string `json:"value"`
	Source   Source `json:"source"`
	Quote    string `json:"quote"`
}

type Issue struct {
	Code     string  `json:"code"`
	Message  string  `json:"message"`
	Scope    Scope   `json:"scope,omitempty"`
	Property string  `json:"property,omitempty"`
	Rules    []Rule  `json:"rules,omitempty"`
	Source   *Source `json:"source,omitempty"`
	Quote    string  `json:"quote,omitempty"`
}

// Every field is optional: nil means there is no unambiguous explicit rule.
// Renderer defaults may only fill nil fields after Issues have been reviewed.
type TextStyle struct {
	FontFamily            *string `json:"font_family,omitempty"`
	FontSizeHalfPoints    *int    `json:"font_size_half_points,omitempty"`
	Color                 *string `json:"color,omitempty"`
	Bold                  *bool   `json:"bold,omitempty"`
	Italic                *bool   `json:"italic,omitempty"`
	Underline             *bool   `json:"underline,omitempty"`
	Shading               *bool   `json:"shading,omitempty"` // false forbids text/table background fill
	CharacterSpacingTwips *int    `json:"character_spacing_twips,omitempty"`
	PositionHalfPoints    *int    `json:"position_half_points,omitempty"`
	LineSpacingTwips      *int    `json:"line_spacing_twips,omitempty"`
	LineRule              *string `json:"line_rule,omitempty"`        // exact or auto
	Alignment             *string `json:"alignment,omitempty"`        // left, center, right, both
	FirstLineChars        *int    `json:"first_line_chars,omitempty"` // hundredths of a character
	SpaceBeforeTwips      *int    `json:"space_before_twips,omitempty"`
	SpaceAfterTwips       *int    `json:"space_after_twips,omitempty"`
}

type PageStyle struct {
	Paper             *string `json:"paper,omitempty"` // A4
	MarginTopTwips    *int    `json:"margin_top_twips,omitempty"`
	MarginBottomTwips *int    `json:"margin_bottom_twips,omitempty"`
	MarginLeftTwips   *int    `json:"margin_left_twips,omitempty"`
	MarginRightTwips  *int    `json:"margin_right_twips,omitempty"`
	Header            *bool   `json:"header,omitempty"`
	Footer            *bool   `json:"footer,omitempty"`
	PageNumbers       *bool   `json:"page_numbers,omitempty"`
}

type Structure struct {
	Cover      *bool `json:"cover,omitempty"`
	TOC        *bool `json:"toc,omitempty"`
	BackCover  *bool `json:"back_cover,omitempty"`
	BlankPages *bool `json:"blank_pages,omitempty"`
}

type Spec struct {
	Scope     Scope     `json:"scope"`
	Body      TextStyle `json:"body"`
	Heading   TextStyle `json:"heading"`
	Table     TextStyle `json:"table"`
	AllText   TextStyle `json:"all_text"`
	Page      PageStyle `json:"page"`
	Structure Structure `json:"structure"`
	Anonymous *bool     `json:"anonymous,omitempty"`
	Rules     []Rule    `json:"rules,omitempty"`
}

type Result struct {
	Rules  []Rule  `json:"rules"`
	Issues []Issue `json:"issues"`
}
