package processor

var GlobalSwears = []string{
	// Race
	`n+i+g+(a+|e+r+)s*`,
	`n[i1]g+`,
	`negros?`,
	`knee ?grows?`,
	`knee ?gears?`,
	`\bcoon\b`,
	`jig+ers?`,
	`wig+ers?`,
	`n[iey1]+(?:\s*|[-_])*?(?:g+|j+)[aeiouy]*(?:r+|y+)?s*`,
	// --
	`chinks?`,
	`zipperheads?`,
	`gooks?`,
	// --
	`wetbacks?`,
	// --
	`kikes?`,
	`heeb`,
	`hebe`,
	`hymie`,
	`goys?`,
	// --
	`gusanos?`,

	// Identity
	`(\bfag+s?\b|fag+ots?)`,
	`fag+s?`,
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
