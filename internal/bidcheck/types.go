// Package bidcheck compares an existing bid against explicit requirements in
// its tender. Unknown requirements and facts are reported for human review.
package bidcheck

import "github.com/Tencent/WeKnora/internal/bidformat"

type Document struct {
	Name   string
	Format string
	Text   string
	Data   []byte
	// VerifiedPages is true only for physical page markers inserted from the
	// authorized parser's page mapping, never for printed or user-written labels.
	VerifiedPages bool
}

type Request struct {
	Tender           Document
	Bid              Document
	Scope            bidformat.Scope
	IdentityKeywords []string
}

type Check struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Message     string `json:"message"`
	Requirement string `json:"requirement"`
	SourceFile  string `json:"source_file"`
	Location    string `json:"location"`
	Excerpt     string `json:"excerpt"`
	Suggestion  string `json:"suggestion"`
	SourcePage  int    `json:"source_page"`
}

type Rule struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	Requirement string `json:"requirement"`
	SourceFile  string `json:"source_file"`
	SourcePage  int    `json:"source_page"`
}

type Summary struct {
	Failed int `json:"failed"`
	Review int `json:"review"`
	Passed int `json:"passed"`
}

type Report struct {
	Status      string   `json:"status"`
	TenderFile  string   `json:"tender_file"`
	BidFile     string   `json:"bid_file"`
	Scope       string   `json:"scope"`
	Checks      []Check  `json:"checks"`
	Rules       []Rule   `json:"rules"`
	Limitations []string `json:"limitations"`
	Summary     Summary  `json:"summary"`
	CheckedAt   string   `json:"checked_at"`
}
