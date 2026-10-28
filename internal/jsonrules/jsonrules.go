// Package jsonrules checks decoded JSON values (as produced by encoding/json into any)
// against the naming, pagination, hypermedia and error rules of the specification.
package jsonrules

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/floriankraemer/http-api-spec-validator/internal/finding"
)

// Rule IDs reported by this package.
const (
	RuleCamelCase  = "naming-camel-case"
	RuleUnderscore = "no-underscore-fields"
	RulePagination = "pagination-shape"
	RuleHypermedia = "hypermedia-shape"
	RuleProblem    = "problem-details"
	RuleErrorCode  = "error-code-format"
	RuleNoDebug    = "no-debug-field"
)

// Vocabulary shared with the OpenAPI checks.
var (
	// LinkFields are the only allowed members of a link object.
	LinkFields = []string{"href", "title"}
	// ActionFields are the only allowed members of an action object.
	ActionFields = []string{"href", "method", "title", "fields"}
	// ActionMethods are the allowed action methods.
	ActionMethods = []string{"POST", "PUT", "PATCH", "DELETE"}
	// FieldFields are the only allowed members of an action field object.
	FieldFields = []string{"name", "type", "required", "title"}
	// FieldTypes are the allowed action field types.
	FieldTypes = []string{"string", "number", "integer", "boolean", "object", "array"}
	// PaginationRequired are the required integer members of the pagination object.
	PaginationRequired = []string{"currentPage", "totalPages", "perPage", "totalItems"}
	// PaginationOptional are the optional integer-or-null members of the pagination object.
	PaginationOptional = []string{"nextPage", "previousPage"}
)

var errorCodeRe = regexp.MustCompile(`^[A-Za-z0-9]+-[A-Za-z0-9]+$`)

func isASCIIUpper(c byte) bool { return c >= 'A' && c <= 'Z' }
func isASCIILower(c byte) bool { return c >= 'a' && c <= 'z' }
func isDigit(c byte) bool      { return c >= '0' && c <= '9' }

// IsCamelCase reports whether name is camelCase: ASCII letters and digits only, starting with a letter.
// A capital first letter is only allowed as a leading abbreviation (ITUser, ID), not PascalCase (User).
func IsCamelCase(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !isASCIIUpper(c) && !isASCIILower(c) && !isDigit(c) {
			return false
		}
	}
	if isDigit(name[0]) {
		return false
	}
	return !isASCIIUpper(name[0]) || (len(name) > 1 && isASCIIUpper(name[1]))
}

// IsControlField reports whether name is a reserved hypermedia member ("_links" or "_actions").
func IsControlField(name string) bool { return name == "_links" || name == "_actions" }

// IsErrorCode reports whether code has the form <component>-<error>, e.g. AP-E0012.
// UUIDs never match because they contain more than one dash.
func IsErrorCode(code string) bool { return errorCodeRe.MatchString(code) }

func sortedKeys(m map[string]any) []string { return slices.Sorted(maps.Keys(m)) }

func mk(rule string, lvl finding.Level, loc, format string, args ...any) finding.Finding {
	return finding.Finding{RuleID: rule, Level: lvl, Location: loc, Message: fmt.Sprintf(format, args...)}
}

func isInteger(v any) bool {
	f, ok := v.(float64)
	return ok && f == math.Trunc(f) && !math.IsInf(f, 0)
}

// CheckKeys reports every object key below v that is not camelCase or starts with '_'.
// The insides of _links and _actions are skipped; CheckHypermedia owns them.
func CheckKeys(v any, ptr string) []finding.Finding {
	var out []finding.Finding
	switch x := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(x) {
			if IsControlField(k) {
				continue
			}
			loc := finding.Pointer(ptr, k)
			switch {
			case strings.HasPrefix(k, "_"):
				out = append(out, mk(RuleUnderscore, finding.Must, loc, "property %q starts with an underscore; only _links and _actions are allowed", k))
			case !IsCamelCase(k):
				out = append(out, mk(RuleCamelCase, finding.Must, loc, "property %q is not camelCase", k))
			}
			out = append(out, CheckKeys(x[k], loc)...)
		}
	case []any:
		for i, e := range x {
			out = append(out, CheckKeys(e, finding.Pointer(ptr, fmt.Sprint(i)))...)
		}
	}
	return out
}

// CheckPagination validates the value of a "pagination" member.
func CheckPagination(v any, ptr string) []finding.Finding {
	m, ok := v.(map[string]any)
	if !ok {
		return []finding.Finding{mk(RulePagination, finding.Must, ptr, "pagination must be an object")}
	}
	var out []finding.Finding
	for _, k := range PaginationRequired {
		val, present := m[k]
		switch {
		case !present:
			out = append(out, mk(RulePagination, finding.Must, ptr, "pagination is missing required property %q", k))
		case !isInteger(val):
			out = append(out, mk(RulePagination, finding.Must, finding.Pointer(ptr, k), "pagination property %q must be an integer", k))
		}
	}
	for _, k := range PaginationOptional {
		if val, present := m[k]; present && val != nil && !isInteger(val) {
			out = append(out, mk(RulePagination, finding.Must, finding.Pointer(ptr, k), "pagination property %q must be an integer or null", k))
		}
	}
	for _, k := range sortedKeys(m) {
		if !slices.Contains(PaginationRequired, k) && !slices.Contains(PaginationOptional, k) {
			out = append(out, mk(RulePagination, finding.Must, finding.Pointer(ptr, k), "pagination has unexpected property %q", k))
		}
	}
	slices.SortStableFunc(out, func(a, b finding.Finding) int { return strings.Compare(a.Location, b.Location) })
	return out
}

// CheckHypermedia validates every _links and _actions member at any depth below v.
func CheckHypermedia(v any, ptr string) []finding.Finding {
	var out []finding.Finding
	switch x := v.(type) {
	case map[string]any:
		for _, k := range sortedKeys(x) {
			loc := finding.Pointer(ptr, k)
			switch k {
			case "_links":
				out = append(out, checkLinks(x[k], loc)...)
			case "_actions":
				out = append(out, checkActions(x[k], loc)...)
			default:
				out = append(out, CheckHypermedia(x[k], loc)...)
			}
		}
	case []any:
		for i, e := range x {
			out = append(out, CheckHypermedia(e, finding.Pointer(ptr, fmt.Sprint(i)))...)
		}
	}
	return out
}

func hm(loc, format string, args ...any) finding.Finding {
	return mk(RuleHypermedia, finding.Must, loc, format, args...)
}

// checkUnknown reports keys of obj not in allowed; what names the object for messages.
func checkUnknown(obj map[string]any, allowed []string, loc, what string) []finding.Finding {
	var out []finding.Finding
	for _, k := range sortedKeys(obj) {
		if !slices.Contains(allowed, k) {
			out = append(out, hm(finding.Pointer(loc, k), "%s has unexpected field %q; %s may only contain %s", what, k, plural(what), strings.Join(allowed, ", ")))
		}
	}
	return out
}

func plural(what string) string {
	// what is e.g. `link "self"`; take the leading noun.
	noun, _, _ := strings.Cut(what, " ")
	return noun + "s"
}

// optString checks an optional string member.
func optString(obj map[string]any, key, loc, what string) []finding.Finding {
	if val, ok := obj[key]; ok {
		if _, isStr := val.(string); !isStr {
			return []finding.Finding{hm(finding.Pointer(loc, key), "%s %q must be a string", what, key)}
		}
	}
	return nil
}

func reqString(obj map[string]any, key, loc, what string) []finding.Finding {
	val, ok := obj[key]
	if !ok {
		return []finding.Finding{hm(loc, "%s is missing required field %q", what, key)}
	}
	if s, isStr := val.(string); !isStr || s == "" {
		return []finding.Finding{hm(finding.Pointer(loc, key), "%s %q must be a non-empty string", what, key)}
	}
	return nil
}

func checkLinks(v any, loc string) []finding.Finding {
	m, ok := v.(map[string]any)
	if !ok {
		return []finding.Finding{hm(loc, "_links must be an object of relation names to links")}
	}
	if len(m) == 0 {
		return []finding.Finding{mk(RuleHypermedia, finding.Should, loc, "_links is empty; omit it or add at least one link")}
	}
	var out []finding.Finding
	for _, rel := range sortedKeys(m) {
		rloc := finding.Pointer(loc, rel)
		if !IsCamelCase(rel) {
			out = append(out, hm(rloc, "link relation %q is not camelCase", rel))
		}
		what := fmt.Sprintf("link %q", rel)
		switch x := m[rel].(type) {
		case []any:
			if len(x) == 0 {
				out = append(out, hm(rloc, "link %q must not be an empty array", rel))
			}
			for i, e := range x {
				out = append(out, checkLink(e, finding.Pointer(rloc, fmt.Sprint(i)), what)...)
			}
		default:
			out = append(out, checkLink(x, rloc, what)...)
		}
	}
	return out
}

func checkLink(v any, loc, what string) []finding.Finding {
	obj, ok := v.(map[string]any)
	if !ok {
		return []finding.Finding{hm(loc, "%s must be an object or an array of objects", what)}
	}
	out := reqString(obj, "href", loc, what)
	out = append(out, optString(obj, "title", loc, what)...)
	return append(out, checkUnknown(obj, LinkFields, loc, what)...)
}

func checkActions(v any, loc string) []finding.Finding {
	m, ok := v.(map[string]any)
	if !ok {
		return []finding.Finding{hm(loc, "_actions must be an object of action names to actions")}
	}
	if len(m) == 0 {
		return []finding.Finding{mk(RuleHypermedia, finding.Should, loc, "_actions is empty; omit it or add at least one action")}
	}
	var out []finding.Finding
	for _, name := range sortedKeys(m) {
		aloc := finding.Pointer(loc, name)
		if !IsCamelCase(name) {
			out = append(out, hm(aloc, "action name %q is not camelCase", name))
		}
		out = append(out, checkAction(m[name], aloc, fmt.Sprintf("action %q", name))...)
	}
	return out
}

func checkAction(v any, loc, what string) []finding.Finding {
	obj, ok := v.(map[string]any)
	if !ok {
		return []finding.Finding{hm(loc, "%s must be an object", what)}
	}
	out := reqString(obj, "href", loc, what)
	switch method, present := obj["method"]; {
	case !present:
		out = append(out, hm(loc, "%s is missing required field %q", what, "method"))
	case !slices.Contains(ActionMethods, fmt.Sprint(method)) || !isString(method): // Sprint is safe: non-strings are rejected by isString
		out = append(out, hm(finding.Pointer(loc, "method"), "%s method must be one of %s", what, strings.Join(ActionMethods, ", ")))
	}
	out = append(out, optString(obj, "title", loc, what)...)
	if f, ok := obj["fields"]; ok {
		out = append(out, checkFieldList(f, finding.Pointer(loc, "fields"), what)...)
	}
	return append(out, checkUnknown(obj, ActionFields, loc, what)...)
}

func isString(v any) bool { _, ok := v.(string); return ok }

func checkFieldList(v any, loc, what string) []finding.Finding {
	list, ok := v.([]any)
	if !ok {
		return []finding.Finding{hm(loc, "%s fields must be an array", what)}
	}
	var out []finding.Finding
	for i, e := range list {
		floc := finding.Pointer(loc, fmt.Sprint(i))
		fw := fmt.Sprintf("%s field %d", what, i)
		obj, ok := e.(map[string]any)
		if !ok {
			out = append(out, hm(floc, "%s must be an object", fw))
			continue
		}
		out = append(out, reqString(obj, "name", floc, fw)...)
		switch t, present := obj["type"]; {
		case !present:
			out = append(out, hm(floc, "%s is missing required field %q", fw, "type"))
		case !isString(t) || !slices.Contains(FieldTypes, t.(string)):
			out = append(out, hm(finding.Pointer(floc, "type"), "%s type must be one of %s", fw, strings.Join(FieldTypes, ", ")))
		}
		if r, ok := obj["required"]; ok {
			if _, isBool := r.(bool); !isBool {
				out = append(out, hm(finding.Pointer(floc, "required"), "%s \"required\" must be a boolean", fw))
			}
		}
		out = append(out, optString(obj, "title", floc, fw)...)
		out = append(out, checkUnknown(obj, FieldFields, floc, "field")...)
	}
	return out
}

// CheckProblem validates an error response body (RFC 9457 problem details plus the spec's
// code, errors and no-debug rules). status is the HTTP status of the response.
func CheckProblem(body map[string]any, status int, ptr string) []finding.Finding {
	var out []finding.Finding
	pd := func(loc, format string, args ...any) {
		out = append(out, mk(RuleProblem, finding.Must, loc, format, args...))
	}
	for _, k := range []string{"type", "title"} {
		val, ok := body[k]
		switch {
		case !ok && k == "type":
			// RFC 9457 defaults a missing type to "about:blank".
			out = append(out, mk(RuleProblem, finding.Should, ptr, "problem details should have a %q member; it defaults to about:blank", k))
		case !ok:
			pd(ptr, "problem details are missing required member %q", k)
		case !isString(val):
			pd(finding.Pointer(ptr, k), "problem member %q must be a string", k)
		}
	}
	switch s, ok := body["status"]; {
	case !ok:
		pd(ptr, "problem details are missing required member %q", "status")
	case !isInteger(s):
		pd(finding.Pointer(ptr, "status"), "problem member \"status\" must be an integer")
	case int(s.(float64)) != status:
		pd(finding.Pointer(ptr, "status"), "problem status %v does not match the HTTP status %d", s, status)
	}
	for _, k := range []string{"detail", "instance"} {
		if val, ok := body[k]; ok && !isString(val) {
			pd(finding.Pointer(ptr, k), "problem member %q must be a string", k)
		}
	}
	if c, ok := body["code"]; ok {
		if s, isStr := c.(string); !isStr || !IsErrorCode(s) {
			out = append(out, mk(RuleErrorCode, finding.Must, finding.Pointer(ptr, "code"), "error code must be a string of the form <component>-<error>, e.g. AP-E0012"))
		}
	}
	if _, ok := body["debug"]; ok {
		out = append(out, mk(RuleNoDebug, finding.Must, finding.Pointer(ptr, "debug"), "error responses must not contain a debug member"))
	}
	if e, ok := body["errors"]; ok {
		out = append(out, checkErrors(e, finding.Pointer(ptr, "errors"))...)
	}
	return out
}

func checkErrors(v any, loc string) []finding.Finding {
	list, ok := v.([]any)
	if !ok {
		return []finding.Finding{mk(RuleProblem, finding.Must, loc, "errors must be an array")}
	}
	var out []finding.Finding
	for i, e := range list {
		eloc := finding.Pointer(loc, fmt.Sprint(i))
		obj, ok := e.(map[string]any)
		if !ok {
			out = append(out, mk(RuleProblem, finding.Must, eloc, "errors item must be an object with detail and pointer"))
			continue
		}
		for _, k := range []string{"detail", "pointer"} {
			val, present := obj[k]
			switch {
			case !present:
				out = append(out, mk(RuleProblem, finding.Must, eloc, "errors item is missing required member %q", k))
			case !isString(val):
				out = append(out, mk(RuleProblem, finding.Must, finding.Pointer(eloc, k), "errors item member %q must be a string", k))
			}
		}
	}
	return out
}
