// Package redaction applies built-in and configured redaction rules.
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

// Rule represents a rule from configuration.
type Rule struct {
	Exact string
	Regex string
}

// New builds an engine and rejects any invalid regular expression.
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
	// Built-in patterns stay active; they are not a substitute for a DLP system.
	builtins := []string{
		`(?i)"(password|passwd|pwd|secret|token|api[_-]?key|authorization|cookie|set-cookie)"\s*:\s*"[^"]*"`,
		`(?i)(((proxy-)?authorization)\s*[:=]\s*)?(bearer|basic)\s+[a-z0-9\-._~+/]+=*`,
		`(?i)(password|passwd|pwd|secret|token|api[_-]?key|authorization|bearer)\s*[=:]\s*("[^"]*"|'[^']*'|\S+)`,
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

// Apply redacts s and returns the text plus the number of replacements.
func (e *Engine) Apply(s string) (string, int) {
	if e == nil || s == "" {
		return s, 0
	}
	out := s
	n := 0
	for _, ex := range e.exact {
		if ex == "" {
			continue
		}
		if strings.Contains(out, ex) {
			c := strings.Count(out, ex)
			out = strings.ReplaceAll(out, ex, replacement)
			n += c
		}
	}
	for _, re := range e.regexps {
		locs := re.FindAllStringIndex(out, -1)
		if len(locs) == 0 {
			continue
		}
		n += len(locs)
		out = re.ReplaceAllString(out, replacement)
	}
	return SanitizeControl(out), n
}

// ApplyAndTruncate sanitizes, redacts, then bounds text in that order.
func ApplyAndTruncate(e *Engine, s string, max int) (string, int, bool) {
	s = SanitizeControl(s)
	redactions := 0
	if e != nil {
		s, redactions = e.Apply(s)
	}
	s, truncated := Truncate(s, max)
	return s, redactions, truncated
}

// SanitizeControl strips control characters except tab and newlines.
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

// Truncate limits the rune count without splitting a UTF-8 character.
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

// TruncateBytes limits UTF-8 size without splitting a character.
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

// UntrustedDataMarker prefixes text that looks like an instruction.
// The content remains available as untrusted analysis data.
const UntrustedDataMarker = "[UNTRUSTED_LOG_DATA]"

// NeutralizeInstructionLike marks text that looks like an instruction
// injection without removing its forensic value.
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
