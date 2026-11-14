package mcp

import "strings"

// authSplitChallenges works around go-sdk v1.8.0's quoted-pair boundary bug.
// Independently implements RFC 9110 sections 5.6.2, 5.6.4 and 11.6.1.
// It only separates header list elements; parameter bytes and order are retained
// for the SDK parser. Remove when a released SDK handles escaped backslashes.
func authSplitChallenges(headers []string) ([]string, error) {
	var out []string
	for _, header := range headers {
		start := 0
		quoted, escaped := false, false
		for i := 0; i < len(header); i++ {
			c := header[i]
			if quoted {
				if escaped {
					escaped = false
					continue
				}
				if c == '\\' {
					escaped = true
					continue
				}
				if c == '"' {
					quoted = false
				}
				continue
			}
			if c == '"' {
				quoted = true
				continue
			}
			if c != ',' {
				continue
			}
			// An auth parameter starts with a token followed by optional BWS and '='.
			j := i + 1
			for j < len(header) && (header[j] == ' ' || header[j] == '\t') {
				j++
			}
			k := j
			for k < len(header) && authChallengeTokenByte(header[k]) {
				k++
			}
			n := k
			for n < len(header) && (header[n] == ' ' || header[n] == '\t') {
				n++
			}
			if k > j && n < len(header) && header[n] == '=' {
				continue
			}
			out = append(out, header[start:i])
			start = i + 1
		}
		if quoted || escaped {
			return nil, ErrOAuthConfiguration
		}
		out = append(out, header[start:])
	}
	return out, nil
}

func authChallengeTokenByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c))
}
