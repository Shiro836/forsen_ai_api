package processor

var GlobalSwears = []string{
	// Race
	`\bn+i+g+(a+|e+r+)s*\b`,
	`\bn[i1]gg.*\b`,
	`\bnegros?\b`,
	`knee ?grows?`,
	`knee ?gears?`,
	`\bcoon\b`,
	`jig+ers?`,
	`wig+ers?`,
	// --
	`chinks?`,
	`zipperheads?`,
	`\bgooks?\b`,
	// --
	`wetbacks?`,
	// --
	`kikes?`,
	`heeb`,
	`hebe`,
	`hymie`,
	`\bgoys?\b`,
	// --
	`gusanos?`,

	// Identity
	`fag+s?`,
	`(\bfag+s?\b|fag+ots?)`,
	`tran+(y|ie)s?`,
	`troons?`,
	`shemales?`,

	// Ableist
	// `tard`,
	// `sperg`,

	`\bkys\b`,
	`kill ?(your|him|her|the[my])sel(f|ves)`,

	`\bg?rap(e|ist|ing|ed)\b`,
}

// cmonBruh
