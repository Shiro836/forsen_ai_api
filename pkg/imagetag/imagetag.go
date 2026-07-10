package imagetag

import (
	"fmt"
	"regexp"
)

// An image reference is either the chat tag <img:code> or a bare site link
// (forsen.fun/i/code preview, forsen.fun/images/code raw). Schemeless and
// this host only, by design.
var tagRe = regexp.MustCompile(`<img:([A-Za-z0-9]{5})>|\bforsen\.fun/(?:i|images)/([A-Za-z0-9]{5})\b`)

func ExtractIDs(s string, max int) []string {
	matches := tagRe.FindAllStringSubmatch(s, -1)
	if len(matches) == 0 {
		return nil
	}

	if max <= 0 || max > len(matches) {
		max = len(matches)
	}

	ids := make([]string, 0, max)
	for _, m := range matches {
		id := m[1]
		if id == "" {
			id = m[2]
		}
		if id == "" {
			continue
		}
		ids = append(ids, id)
		if len(ids) == max {
			break
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

func ReplaceImageTags(s string) string {
	idx := 0
	return tagRe.ReplaceAllStringFunc(s, func(_ string) string {
		idx++
		return fmt.Sprintf("image_%d", idx)
	})
}

// ReplaceID replaces the first reference to id, in any form, with repl.
func ReplaceID(s, id, repl string) string {
	quoted := regexp.QuoteMeta(id)
	re := regexp.MustCompile(`<img:` + quoted + `>|\bforsen\.fun/(?:i|images)/` + quoted + `\b`)

	loc := re.FindStringIndex(s)
	if loc == nil {
		return s
	}

	return s[:loc[0]] + repl + s[loc[1]:]
}
