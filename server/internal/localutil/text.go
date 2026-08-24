package localutil

import (
	"regexp"
	"strings"
)

var MentionRe = regexp.MustCompile(`\[@?(.+?)\]\(mention://(member|agent|squad|issue|all)/([0-9a-fA-F-]+|all)\)`)

func UnescapeBackslashEscapes(value string) string {
	if !strings.ContainsRune(value, '\\') {
		return value
	}
	var result strings.Builder
	result.Grow(len(value))
	for index := 0; index < len(value); index++ {
		current := value[index]
		if current == '\\' && index+1 < len(value) {
			switch value[index+1] {
			case 'n':
				result.WriteByte('\n')
				index++
				continue
			case 'r':
				result.WriteByte('\r')
				index++
				continue
			case 't':
				result.WriteByte('\t')
				index++
				continue
			case '\\':
				result.WriteByte('\\')
				index++
				continue
			}
		}
		result.WriteByte(current)
	}
	return result.String()
}
