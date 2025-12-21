package imagetag

import (
	"fmt"
	"regexp"
)

var tagRe = regexp.MustCompile(`<img:([A-Za-z0-9]{5})>`)

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
		if len(m) < 2 {
			continue
		}
		ids = append(ids, m[1])
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
