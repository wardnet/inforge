// Package pathglob compiles the restricted absolute-path glob syntax authored
// in a service's mesh.public_paths / mesh.internal_paths (and consumed by the
// gateway's derived routing table) into anchored regexes for nginx `location ~`
// blocks, and answers the overlap questions validation needs.
//
// Syntax (ADR-0034):
//   - a pattern is an absolute path: `/` followed by `/`-separated segments;
//   - a segment mixes literal characters ([A-Za-z0-9._-]) and `*` wildcards,
//     each `*` matching one or more non-`/` characters (`[^/]+`);
//   - the final segment may be exactly `**`, matching the parent node itself
//     and any deeper suffix (`(/.*)?`).
//
// The package is dependency-free (stdlib only) on purpose: internal/validate,
// internal/nginx, and internal/meshnginx all consume it, so it must never grow
// an import that could cycle (the meshpaths idiom).
package pathglob

import (
	"fmt"
	"regexp"
	"strings"
)

// Pattern is a parsed, validated path glob. The zero value is not usable;
// obtain one via Parse.
type Pattern struct {
	raw      string
	segments []string
	tail     bool // trailing "/**": matches the parent node and any suffix
	re       *regexp.Regexp
}

// segmentChars are the characters a segment may contain besides `*` (D6): the
// compiled regex must be emittable as a bare nginx/crossplane argument, so the
// charset excludes anything that would need quoting.
const segmentChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-"

// Parse validates a path glob and compiles it. Every rejection names the
// offending element so validation can surface the message verbatim.
func Parse(s string) (Pattern, error) {
	if s == "" || s == "/" {
		return Pattern{}, fmt.Errorf("a path glob must be a non-root absolute path (e.g. /v1/account/**)")
	}
	if !strings.HasPrefix(s, "/") {
		return Pattern{}, fmt.Errorf("path must start with %q", "/")
	}
	if strings.HasSuffix(s, "/") {
		return Pattern{}, fmt.Errorf("path must not end with %q; a subtree claim is spelled /prefix/**", "/")
	}
	segments := strings.Split(s[1:], "/")
	tail := false
	for i, seg := range segments {
		last := i == len(segments)-1
		switch {
		case seg == "":
			return Pattern{}, fmt.Errorf("empty path segment (%q)", "//")
		case seg == "." || seg == "..":
			return Pattern{}, fmt.Errorf("%q segments are not allowed", seg)
		case seg == "**":
			if !last {
				return Pattern{}, fmt.Errorf("%q must be the final path segment", "**")
			}
			tail = true
		case strings.Contains(seg, "**"):
			return Pattern{}, fmt.Errorf("segment %q is invalid: %q must stand alone as the final segment", seg, "**")
		default:
			for _, r := range seg {
				if r != '*' && !strings.ContainsRune(segmentChars, r) {
					return Pattern{}, fmt.Errorf("segment %q contains %q; allowed characters are [A-Za-z0-9._-] and %q", seg, string(r), "*")
				}
			}
		}
	}
	if tail {
		segments = segments[:len(segments)-1]
		// A bare "/**" is the root pattern in disguise (^(/.*)?$ matches every
		// request path): rejecting "/" while admitting its semantic equivalent
		// would make the non-root guard cosmetic and silently defeat the
		// closed-by-default posture (ADR-0034).
		if len(segments) == 0 {
			return Pattern{}, fmt.Errorf("%q matches every request path; declare concrete path globs (e.g. /v1/**)", "/**")
		}
	}
	p := Pattern{raw: s, segments: segments, tail: tail}
	re, err := regexp.Compile(p.Regex())
	if err != nil {
		// Unreachable for a pattern that passed the checks above; guard anyway.
		return Pattern{}, fmt.Errorf("compiling %q: %w", s, err)
	}
	p.re = re
	return p, nil
}

// CheckExact validates an exact (glob-free) absolute path, e.g. a declared
// health probe path: leading "/", no trailing "/", non-empty segments, no "."
// or ".." segments, and every character in the same restricted charset the
// glob syntax uses — nginx matches locations against the decoded,
// query-stripped URI, so characters like "?" or "#" can never appear in a
// matched path and would render a location that silently never fires.
func CheckExact(p string) error {
	if p == "" || p == "/" {
		return fmt.Errorf("an exact path must be a non-root absolute path (e.g. /healthz)")
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("path must start with %q", "/")
	}
	if strings.HasSuffix(p, "/") {
		return fmt.Errorf("path must not end with %q", "/")
	}
	for _, seg := range strings.Split(p[1:], "/") {
		switch seg {
		case "":
			return fmt.Errorf("empty path segment (%q)", "//")
		case ".", "..":
			return fmt.Errorf("%q segments are not allowed", seg)
		}
		for _, r := range seg {
			if !strings.ContainsRune(segmentChars, r) {
				return fmt.Errorf("segment %q contains %q; allowed characters are [A-Za-z0-9._-] (globs are not allowed in an exact path)", seg, string(r))
			}
		}
	}
	return nil
}

// String returns the raw glob as authored.
func (p Pattern) String() string { return p.raw }

// Regex returns the fully anchored regex for an nginx `location ~` matcher,
// e.g. /v*/account/** -> ^/v[^/]+/account(/.*)?$.
func (p Pattern) Regex() string {
	var b strings.Builder
	b.WriteString("^")
	for _, seg := range p.segments {
		b.WriteString("/")
		b.WriteString(segmentRegex(seg))
	}
	if p.tail {
		b.WriteString("(/.*)?")
	}
	b.WriteString("$")
	return b.String()
}

// Matches reports whether an exact request path matches the pattern. Used by
// validation to reject a gateway health probe path shadowed by a public glob.
func (p Pattern) Matches(path string) bool { return p.re.MatchString(path) }

// segmentRegex compiles one segment: literal runs are quoted, each `*` becomes
// one-or-more non-slash characters (D4).
func segmentRegex(seg string) string {
	var b strings.Builder
	for i, part := range strings.Split(seg, "*") {
		if i > 0 {
			b.WriteString("[^/]+")
		}
		b.WriteString(regexp.QuoteMeta(part))
	}
	return b.String()
}

// Overlaps reports whether some concrete request path matches both patterns —
// the load-bearing invariant for a gateway's derived routing table (D7): two
// services on one gateway must not claim overlapping public paths, or emission
// order would pick the winner silently.
func (p Pattern) Overlaps(o Pattern) bool {
	n := len(p.segments)
	if len(o.segments) < n {
		n = len(o.segments)
	}
	for i := 0; i < n; i++ {
		if !segmentsOverlap(p.segments[i], o.segments[i]) {
			return false
		}
	}
	// Same depth: a witness exists (segments are independent). Different depth:
	// the shorter pattern reaches the longer one's extra segments only through
	// a trailing `**` (which matches any suffix, including none).
	switch {
	case len(p.segments) == len(o.segments):
		return true
	case len(p.segments) < len(o.segments):
		return p.tail
	default:
		return o.tail
	}
}

// segmentsOverlap reports whether two single segments can match a common
// string. Each segment is a sequence of literal characters and `*` wildcards
// (one-or-more arbitrary non-slash characters). A `*` is modeled as one
// mandatory arbitrary character followed by an optional zero-or-more loop, and
// the check is a product-automaton reachability walk: states are position
// pairs (i, j) into the two token lists, a step consumes one character both
// sides accept (two literals must be equal; a wildcard accepts anything — the
// alphabet is large, so a compatible character always exists).
func segmentsOverlap(a, b string) bool {
	ta, tb := tokens(a), tokens(b)
	type state struct{ i, j int }
	seen := map[state]bool{}
	stack := []state{{0, 0}}
	for len(stack) > 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[s] {
			continue
		}
		seen[s] = true
		aEnd, bEnd := s.i == len(ta), s.j == len(tb)
		// Epsilon moves: a zero-or-more loop token may be skipped.
		if !aEnd && ta[s.i] == tokStar0 {
			stack = append(stack, state{s.i + 1, s.j})
		}
		if !bEnd && tb[s.j] == tokStar0 {
			stack = append(stack, state{s.i, s.j + 1})
		}
		if aEnd && bEnd {
			return true
		}
		if aEnd || bEnd {
			continue
		}
		x, y := ta[s.i], tb[s.j]
		// Two literals must agree; any wildcard side accepts the other's char.
		if x >= 0 && y >= 0 && x != y {
			continue
		}
		ni, nj := s.i+1, s.j+1
		if x == tokStar0 {
			ni = s.i // the loop consumes and stays
		}
		if y == tokStar0 {
			nj = s.j
		}
		stack = append(stack, state{ni, nj})
		// A looping token can also consume this char and then exit.
		if x == tokStar0 {
			stack = append(stack, state{s.i + 1, nj})
		}
		if y == tokStar0 {
			stack = append(stack, state{ni, s.j + 1})
		}
	}
	return false
}

// Token encoding for segmentsOverlap: literal characters are their (non-negative)
// rune value; wildcards expand to one mandatory any-character token followed by
// a zero-or-more loop token.
const (
	tokAny   = rune(-1) // exactly one arbitrary character
	tokStar0 = rune(-2) // zero or more arbitrary characters (self-loop)
)

func tokens(seg string) []rune {
	var out []rune
	for _, r := range seg {
		if r == '*' {
			out = append(out, tokAny, tokStar0)
		} else {
			out = append(out, r)
		}
	}
	return out
}
