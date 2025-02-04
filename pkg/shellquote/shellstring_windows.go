package shellquote

import (
	"io"
	"strings"
)

func quoteArg(arg string) string {
	return Windows(arg)
}

// Split the given string into an array, using shell quote semantics.
func Split(line string) ([]string, error) {
	if line == "" {
		return nil, nil
	}

	sb := strings.Builder{}
	slashes := 0
	slashOut := func() {
		for ; slashes > 0; slashes-- {
			sb.WriteByte('\\')
		}
	}
	slashOutBeforeQuote := func() bool {
		even := slashes&1 == 0
		slashes >>= 1
		slashOut()
		return even
	}
	parseDQSegment := func(s string) (string, int) {
		slashes = 0
		for i, r := range s {
			switch r {
			case '"':
				if slashOutBeforeQuote() {
					return sb.String(), i + 2
				}
				sb.WriteRune(r)
			case '\\':
				slashes++
			default:
				slashOut()
				sb.WriteRune(r)
			}
		}
		return "", -1
	}
	parseUQSegment := func(s string) (string, int) {
		slashes = 0
		for i, r := range s {
			switch r {
			case ' ', '\t', '\r', '\n': // start of quoted string or whitespace ends this segment
				slashOut()
				return sb.String(), i
			case '"':
				if slashOutBeforeQuote() {
					return sb.String(), i
				}
				sb.WriteByte('"')
			case '\\':
				slashes++
			default:
				slashOut()
				sb.WriteRune(r)
			}
		}
		return sb.String(), len(s)
	}

	var ss []string
	e := -1
	newArg := true
	for i, r := range line {
		if i < e {
			continue
		}
		var s string
		var x int
		switch r {
		case ' ', '\t', '\r', '\n':
			// skip whitespace
			sb.Reset()
			newArg = true
			continue
		case '"':
			s, x = parseDQSegment(line[i+1:])
		default:
			s, x = parseUQSegment(line[i:])
		}
		if x < 0 {
			return nil, io.ErrUnexpectedEOF
		}
		e = i + x
		if newArg {
			ss = append(ss, s)
			newArg = false
		} else {
			ss[len(ss)-1] = s
		}
	}
	return ss, nil
}
