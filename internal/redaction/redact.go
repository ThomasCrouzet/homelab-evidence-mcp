// Package redaction uses built-in rules and rules from the configuration.
package redaction

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const replacement = "[REDACTED]"

// Engine holds compiled redaction rules.
type Engine struct {
	exact   []string
	regexps []*regexp.Regexp
}

// Rule contains a rule from the configuration.
type Rule struct {
	Exact string
	Regex string
}

// New makes an engine and gives an error for each incorrect regular expression.
func New(rules []Rule) (*Engine, error) {
	e := &Engine{}
	for i, r := range rules {
		if r.Exact != "" {
			e.exact = append(e.exact, r.Exact)
		}
		if r.Regex != "" {
			re, err := regexp.Compile(r.Regex)
			if err != nil {
				return nil, fmt.Errorf("redaction rule[%d] regex: %w", i, err)
			}
			e.regexps = append(e.regexps, re)
		}
	}
	// Built-in patterns remain active. They do not replace a DLP system.
	builtins := []string{
		`(?i)"(password|passwd|pwd|secret|token|api[_-]?key|authorization|cookie|set-cookie)"\s*:\s*"(?:\\.|[^"\\])*"`,
		`(?i)(((proxy-)?authorization)\s*[:=]\s*)?(bearer|basic)\s+[a-z0-9\-._~+/]+=*`,
		`(?i)(password|passwd|pwd|secret|token|api[_-]?key|authorization|bearer)\s*[=:]\s*("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|\S+)`,
		`(?i)(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}`,
		`(?i)sk-[A-Za-z0-9]{20,}`,
		`(?i)AKIA[0-9A-Z]{16}`,
		`(?is)-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----.*?-----END (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`,
		`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`,
		`(?i)(cookie|set-cookie)\s*[:=]\s*\S+`,
	}
	for _, p := range builtins {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("builtin redaction: %w", err)
		}
		e.regexps = append(e.regexps, re)
	}
	return e, nil
}

// Apply redacts s and gives the text plus the number of merged redacted regions.
func (e *Engine) Apply(s string) (string, int) {
	if e == nil || s == "" {
		return s, 0
	}
	// Match each rule against the original text. This prevents rules from
	// processing replacement markers that earlier rules inserted.
	redacted := make([]bool, len(s))
	covered := 0
	mark := func(start, end int) {
		if start < 0 || end > len(redacted) || start >= end {
			return
		}
		for i := start; i < end; i++ {
			if !redacted[i] {
				redacted[i] = true
				covered++
			}
		}
	}
	for _, ex := range e.exact {
		if ex == "" {
			continue
		}
		for offset := 0; offset <= len(s)-len(ex); {
			index := strings.Index(s[offset:], ex)
			if index < 0 {
				break
			}
			start := offset + index
			end := start + len(ex)
			mark(start, end)
			offset = end
		}
		if covered == len(s) {
			break
		}
	}
	for _, re := range e.regexps {
		if covered == len(s) {
			break
		}
		// Search the complete input once. Slicing changes the meaning of
		// anchors and word boundaries at each new slice boundary.
		for _, loc := range re.FindAllStringIndex(s, -1) {
			start := loc[0]
			end := loc[1]
			if end > start {
				mark(start, end)
			}
		}
	}

	var out strings.Builder
	out.Grow(len(s))
	regions := 0
	last := 0
	for i := 0; i < len(redacted); {
		if !redacted[i] {
			i++
			continue
		}
		start := i
		for i < len(redacted) && redacted[i] {
			i++
		}
		out.WriteString(s[last:start])
		out.WriteString(replacement)
		last = i
		regions++
	}
	if regions == 0 {
		return SanitizeControl(s), 0
	}
	out.WriteString(s[last:])
	return SanitizeControl(out.String()), regions
}

// ApplyAndTruncate sanitizes and redacts the text. It then sets a maximum size.
func ApplyAndTruncate(e *Engine, s string, max int) (string, int, bool) {
	s = SanitizeControl(s)
	redactions := 0
	if e != nil {
		s, redactions = e.Apply(s)
	}
	s, truncated := Truncate(s, max)
	return s, redactions, truncated
}

// SanitizeControl removes control characters except tab and newlines.
func SanitizeControl(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToValidUTF8(s, " ")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteRune(r)
		case unicode.IsControl(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Truncate sets a maximum rune count and does not divide a UTF-8 character.
func Truncate(s string, max int) (string, bool) {
	if max <= 0 {
		return "", true
	}
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	runes := []rune(s)
	if max < 3 {
		return string(runes[:max]), true
	}
	return string(runes[:max-3]) + "...", true
}

// TruncateBytes sets a maximum UTF-8 size and does not divide a character.
func TruncateBytes(s string, max int) (string, bool) {
	if max <= 0 {
		return "", true
	}
	if len(s) <= max {
		return s, false
	}
	suffix := "..."
	target := max - len(suffix)
	if target < 0 {
		target = max
		suffix = ""
	}
	end := 0
	for index := range s {
		if index > target {
			break
		}
		end = index
	}
	if end == 0 && target >= len(s) {
		end = len(s)
	}
	return s[:end] + suffix, true
}

// UntrustedDataMarker adds a prefix to text that looks like an instruction.
// The content remains available as untrusted analysis data.
const UntrustedDataMarker = "[UNTRUSTED_LOG_DATA]"

// NeutralizeInstructionLike marks text that looks like an instruction
// injection and keeps its forensic value.
func NeutralizeInstructionLike(s string) string {
	if s == "" {
		return s
	}
	if strings.HasPrefix(s, UntrustedDataMarker) {
		return s
	}
	lower := strings.ToLower(s)
	markers := []string{
		"ignore previous instructions",
		"ignore all instructions",
		"you are now",
		"execute the following",
		"run this command",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return UntrustedDataMarker + " " + s
		}
	}
	return s
}
