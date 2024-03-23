package tools

import (
	"io"
	"strings"
	"unicode/utf8"
)

func DrainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}

func IReplace(s, old, new string) string { // replace all, case insensitive
	if old == new || old == "" {
		return s // avoid allocation
	}
	t := strings.ToLower(s)
	o := strings.ToLower(old)

	// Compute number of replacements.
	n := strings.Count(t, o)
	if n == 0 {
		return s // avoid allocation
	}
	// Apply replacements to buffer.
	var b strings.Builder
	b.Grow(len(s) + n*(len(new)-len(old)))
	start := 0
	for i := 0; i < n; i++ {
		j := start
		if len(old) == 0 {
			if i > 0 {
				_, wid := utf8.DecodeRuneInString(s[start:])
				j += wid
			}
		} else {
			j += strings.Index(t[start:], o)
		}
		b.WriteString(s[start:j])
		b.WriteString(new)
		start = j + len(old)
	}
	b.WriteString(s[start:])
	return b.String()
}
