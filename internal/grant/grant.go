// Package grant models a Grant: a service's declared, permissioned access to a
// Grantable resource, materialized as the env vars / files the service composes
// over the fields the resource publishes (ADR-0025).
//
// The package unifies "what fields a (resource, permission) publishes" behind the
// Grantable interface, and provides the credential-free template machinery the
// validator uses to check a grant's outputs: without any provider credentials.
// The actual materialization (Grant) is provider work that lands in the later
// slices of #117 — the Grant methods here are deferred stubs.
package grant

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// Permission is the universal grant permission. Each Grantable maps it to its own
// domain (a Database maps ro/rw to a read-only/read-write DB user; a PKI resource
// maps ro/rw to verify/issue — the CA cert vs. the signing key).
type Permission string

const (
	// PermissionRO is the consume/trust credential (read-only DB user; PKI verify
	// — the CA cert only).
	PermissionRO Permission = "ro"
	// PermissionRW is the productive/dangerous credential (read-write DB user; PKI
	// issue — the root signing key).
	PermissionRW Permission = "rw"
)

// Valid reports whether p is one of the two recognized permissions.
func (p Permission) Valid() bool {
	return p == PermissionRO || p == PermissionRW
}

// FileMaterial is a file field's PEM payload: the bytes a Grant produces to be
// written to the secrets provider and projected to the service's tmpfs RuntimeDir
// as an on-host PEM file, reusing the descriptor files: / projectFiles mechanism
// (ADR-0024 / slice #109). A file-field output env var holds the projected path,
// not the PEM. The projection wiring lands with the PKI resource Grant (slice C of
// #117); this slice only defines the shape.
type FileMaterial struct {
	PEM pulumi.StringOutput
}

// Fields is the set of named credential-material pieces a Grant produces for a
// permission. A value field is a string composed into a secret at deploy time
// (the ADR-0010 env-secret path); a file field is PEM material written and
// projected as an on-host file. The two never mix within one output template.
type Fields struct {
	Values map[string]pulumi.StringOutput // value fields: USER, PASSWORD, …
	Files  map[string]FileMaterial        // file fields: CERT, KEY (PEM to write + project)
}

// Grantable is a resource type a service can be granted a permission on. Grant
// materializes the credential during the Pulumi program (DB users created,
// secrets written); FieldNames answers "which fields does (this type, this
// permission) publish" without any credentials, for credential-free validation.
type Grantable interface {
	// Grant materializes the credential for service under perm in (env, region)
	// and returns the resulting fields. It runs inside the Pulumi program.
	Grant(ctx *pulumi.Context, service string, perm Permission, env, region string) (Fields, error)
	// FieldNames returns the value- and file-field names this type publishes for
	// perm. Credential-free and instance-independent: it depends only on the
	// resource type and permission, so the validator may call it on a zero-value
	// Grantable obtained from For.
	FieldNames(perm Permission) (values, files []string)
}

// For returns the Grantable implementation for a resource type token ("database"
// or "pki") and whether the type is a supported grantable. The returned value
// carries no instance state, so it is suitable for credential-free FieldNames
// calls during validation.
func For(resourceType string) (Grantable, bool) {
	switch resourceType {
	case "database":
		return Database{}, true
	case "pki":
		return PKIResource{}, true
	default:
		return nil, false
	}
}

// fieldNameRE matches a well-formed {FIELD} placeholder name.
var fieldNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// segment is one piece of a parsed template: either literal text (literal set) or
// a {field} placeholder (field set). Exactly one of the two is non-empty.
type segment struct {
	literal string
	field   string
}

// Template is a parsed outputs template — an ordered sequence of literal and
// {FIELD} placeholder segments. Braces are reserved for placeholders; a literal
// brace is not supported.
type Template struct {
	segments []segment
}

// ParseTemplate parses an outputs template into literal and placeholder segments,
// rejecting unbalanced braces and empty or malformed placeholder names.
func ParseTemplate(s string) (Template, error) {
	var segs []segment
	var lit strings.Builder
	for i := 0; i < len(s); {
		switch s[i] {
		case '{':
			rel := strings.IndexByte(s[i+1:], '}')
			if rel < 0 {
				return Template{}, fmt.Errorf("unbalanced '{' in template %q", s)
			}
			name := s[i+1 : i+1+rel]
			if !fieldNameRE.MatchString(name) {
				return Template{}, fmt.Errorf("invalid placeholder {%s} in template %q (want {FIELD})", name, s)
			}
			if lit.Len() > 0 {
				segs = append(segs, segment{literal: lit.String()})
				lit.Reset()
			}
			segs = append(segs, segment{field: name})
			i += rel + 2
		case '}':
			return Template{}, fmt.Errorf("unbalanced '}' in template %q", s)
		default:
			lit.WriteByte(s[i])
			i++
		}
	}
	if lit.Len() > 0 {
		segs = append(segs, segment{literal: lit.String()})
	}
	return Template{segments: segs}, nil
}

// Fields returns the placeholder field names referenced, in order, with
// duplicates preserved.
func (t Template) Fields() []string {
	var out []string
	for _, s := range t.segments {
		if s.field != "" {
			out = append(out, s.field)
		}
	}
	return out
}

// HasLiteral reports whether the template contains any literal (non-placeholder)
// text. A file-field template must have no literal text and exactly one
// placeholder (the field is a path, not a substring).
func (t Template) HasLiteral() bool {
	for _, s := range t.segments {
		if s.literal != "" {
			return true
		}
	}
	return false
}

// Interpolate substitutes each placeholder with its value from values, returning
// the composed string. It errors if a referenced field has no value.
func (t Template) Interpolate(values map[string]string) (string, error) {
	var b strings.Builder
	for _, s := range t.segments {
		if s.field == "" {
			b.WriteString(s.literal)
			continue
		}
		v, ok := values[s.field]
		if !ok {
			return "", fmt.Errorf("no value for field %q", s.field)
		}
		b.WriteString(v)
	}
	return b.String(), nil
}
