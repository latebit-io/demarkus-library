package domain

// MatchBody asks LOOKUP for section-level text matches instead of the
// catalog's tag and title match. A world without body match answers from the
// catalog instead, which CatalogResult.BodyMatched reports.
const MatchBody = "body"

// CatalogQuery is one ranked catalog search. World "" spans every readable
// world; Tag narrows to documents carrying it; Match is "" or MatchBody.
type CatalogQuery struct {
	World string
	Query string
	Tag   string
	Match string
	Limit int
}

// CatalogHit is one ranked LOOKUP row. A body match names the section that
// matched (Anchor) and an excerpt of it (Snippet); a catalog match has neither.
type CatalogHit struct {
	Ref
	Anchor     string
	Title      string
	Tags       []string
	Status     string
	Importance float64
	Snippet    string
}

// CatalogResult is a catalog search's rows in rank order. BodyMatched is false
// when a MatchBody request was answered from the catalog.
type CatalogResult struct {
	Hits        []CatalogHit
	BodyMatched bool
}
