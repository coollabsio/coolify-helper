// Package jsonc reads JSON with comments and trailing commas (tsconfig.json,
// turbo.json, nx.json, bun.lock), by rewriting it to standard JSON first.
package jsonc

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Standardize removes comments and trailing commas outside strings, and a
// leading byte order mark. It fails on an unterminated comment or string.
func Standardize(b []byte) ([]byte, error) {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c == '"':
			j := i + 1
			for ; j < len(b); j++ {
				if b[j] == '\\' {
					j++
					continue
				}
				if b[j] == '"' {
					break
				}
			}
			if j >= len(b) {
				return nil, errors.New("unterminated string")
			}
			out = append(out, b[i:j+1]...)
			i = j
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			if i < len(b) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			end := bytes.Index(b[i+2:], []byte("*/"))
			if end < 0 {
				return nil, errors.New("unterminated comment")
			}
			out = append(out, ' ')
			i += end + 3
		case c == ']' || c == '}':
			// Drop a trailing comma before the closing bracket.
			k := len(out) - 1
			for k >= 0 && isSpace(out[k]) {
				k--
			}
			if k >= 0 && out[k] == ',' {
				out = append(out[:k], out[k+1:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out, nil
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// Unmarshal parses JSONC into v.
func Unmarshal(b []byte, v any) error {
	std, err := Standardize(b)
	if err != nil {
		return err
	}
	return json.Unmarshal(std, v)
}
