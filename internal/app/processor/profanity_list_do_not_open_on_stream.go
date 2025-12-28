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

	// china n and g chars
	`内`,
	`那`,
	`哪`,
	`个`,
	`阁`,

	// korean
	`내`,
	`네`,
	`가`,
	`거`,
	`니`,

	// arab
	`نيجا`,
	`نيج`,
	`ن`,
	`ي`,
	`ج`,
	`ا`,
}

// cmonBruh
