// Package openapi lints OpenAPI 3.0/3.1 documents against the API design rules of the specification.
package openapi

import (
	"errors"
	"fmt"
	"mime"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pb33f/go-yaml"
	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/orderedmap"

	"github.com/floriankraemer/http-api-spec/validator/internal/finding"
	"github.com/floriankraemer/http-api-spec/validator/internal/jsonrules"
)

// Rule IDs reported by Lint in addition to the ones shared with package jsonrules.
const (
	RuleVersionPath = "version-path-prefix"
	RuleLocale      = "locale-not-in-url"
	RuleEnvelope    = "success-data-envelope"
	RuleProblemJSON = "error-problem-json"
	RuleIdempotent  = "idempotent-tag"
	RuleNoBatch     = "no-batch-requests"
	RuleBearer      = "bearer-jwt-auth" //nolint:gosec // G101: rule ID, not a credential
)

// maxDepth bounds allOf flattening and nested hypermedia walks; real documents are far shallower.
const maxDepth = 16

var (
	versionSegmentRe = regexp.MustCompile(`^v\d+$`)
	localeLiteralRe  = regexp.MustCompile(`^[a-z]{2}-[A-Z]{2}$`) // region form only; bare "en" collides with names like "me"
	localeNames      = []string{"lang", "locale", "language"}
)

// Lint checks an OpenAPI 3.x document (YAML or JSON) and returns its findings sorted with finding.Sort.
// It returns an error only when data cannot be parsed or is not an OpenAPI 3.x document.
func Lint(data []byte) ([]finding.Finding, error) {
	d, err := libopenapi.NewDocument(data)
	if err != nil {
		return nil, fmt.Errorf("parse document: %w", err)
	}
	model, err := d.BuildV3Model()
	if model == nil {
		return nil, fmt.Errorf("not an OpenAPI 3.x document: %w", errors.Join(err, errors.New("no model")))
	}
	// Non-nil model with err: unresolvable refs or circular references; the model is still usable.
	l := &linter{root: d.GetSpecInfo().RootNode, seen: map[string]bool{}, visited: map[string]bool{}}
	l.run(&model.Model)
	finding.Sort(l.out)
	return l.out, nil
}

type linter struct {
	root       *yaml.Node
	docServers []*v3.Server
	out        []finding.Finding
	seen       map[string]bool // dedupe key: rule, location, message
	visited    map[string]bool // schema locations already walked
}

func (l *linter) add(rule string, lvl finding.Level, loc, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	key := rule + "\x00" + loc + "\x00" + msg
	if l.seen[key] {
		return
	}
	l.seen[key] = true
	l.out = append(l.out, finding.Finding{RuleID: rule, Level: lvl, Location: loc, Line: l.lineOf(loc), Message: msg})
}

func (l *linter) must(rule, loc, format string, args ...any) {
	l.add(rule, finding.Must, loc, format, args...)
}

// lineOf resolves a JSON Pointer to the source line of its key; for pointers that
// do not exist it returns the line of the deepest ancestor that does (document start at worst).
func (l *linter) lineOf(loc string) int {
	n := l.root
	if n != nil && n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	line := 0
	if n != nil {
		line = n.Line // missing top-level keys are attributed to the document start
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(loc, "#"), "/")
	if rest == "" {
		return line
	}
	for _, tok := range strings.Split(rest, "/") {
		tok = strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
		var next *yaml.Node
		switch {
		case n == nil:
		case n.Kind == yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				if n.Content[i].Value == tok {
					line, next = n.Content[i].Line, n.Content[i+1]
					break
				}
			}
		case n.Kind == yaml.SequenceNode:
			if i, err := strconv.Atoi(tok); err == nil && i >= 0 && i < len(n.Content) {
				line, next = n.Content[i].Line, n.Content[i]
			}
		}
		if next == nil {
			break
		}
		n = next
	}
	return line
}

func (l *linter) run(doc *v3.Document) {
	l.docServers = doc.Servers
	l.checkBearer(doc)
	if doc.Components != nil {
		l.walkComponents(doc.Components)
	}
	if doc.Paths == nil {
		return
	}
	for path, item := range doc.Paths.PathItems.FromOldest() {
		l.checkPathItem(path, item)
	}
}

// ---- schema walking: naming, underscore and hypermedia rules ----

// deref returns the schema behind sp and its definition-site location: the target of a
// local $ref, otherwise loc itself.
func deref(sp *base.SchemaProxy, loc string) (*base.Schema, string) {
	if sp == nil {
		return nil, loc
	}
	if sp.IsReference() {
		if ref := sp.GetReference(); strings.HasPrefix(ref, "#/") {
			loc = ref
		}
	}
	return sp.Schema(), loc
}

func (l *linter) walkSchema(sp *base.SchemaProxy, loc string) {
	s, loc := deref(sp, loc)
	if s == nil || l.visited[loc] {
		return
	}
	l.visited[loc] = true
	for name, p := range s.Properties.FromOldest() {
		ploc := finding.Pointer(loc, "properties", name)
		switch {
		case jsonrules.IsControlField(name):
			if name == "_links" {
				l.checkLinks(p, ploc)
			} else {
				l.checkActions(p, ploc)
			}
		case strings.HasPrefix(name, "_"):
			l.must(jsonrules.RuleUnderscore, ploc, "property %q starts with an underscore; only _links and _actions are allowed", name)
		case !jsonrules.IsCamelCase(name):
			l.must(jsonrules.RuleCamelCase, ploc, "property %q is not camelCase", name)
		}
		l.walkSchema(p, ploc)
	}
	for _, g := range []struct {
		key  string
		list []*base.SchemaProxy
	}{{"allOf", s.AllOf}, {"oneOf", s.OneOf}, {"anyOf", s.AnyOf}, {"prefixItems", s.PrefixItems}} {
		for i, sub := range g.list {
			l.walkSchema(sub, finding.Pointer(loc, g.key, strconv.Itoa(i)))
		}
	}
	if s.Items != nil && s.Items.IsA() {
		l.walkSchema(s.Items.A, finding.Pointer(loc, "items"))
	}
	if s.AdditionalProperties != nil && s.AdditionalProperties.IsA() {
		l.walkSchema(s.AdditionalProperties.A, finding.Pointer(loc, "additionalProperties"))
	}
	l.walkSchema(s.Not, finding.Pointer(loc, "not"))
}

func (l *linter) walkContent(content mediaTypes, loc string) {
	for mt, m := range content.FromOldest() {
		if m != nil {
			l.walkSchema(m.Schema, finding.Pointer(loc, "content", mt, "schema"))
		}
	}
}

func (l *linter) walkParam(p *v3.Parameter, loc string) {
	if p == nil || p.IsReference() {
		return
	}
	l.walkSchema(p.Schema, finding.Pointer(loc, "schema"))
	l.walkContent(p.Content, loc)
}

func (l *linter) walkResponse(r *v3.Response, loc string) {
	if r == nil || r.IsReference() {
		return
	}
	l.walkContent(r.Content, loc)
}

func (l *linter) walkRequestBody(b *v3.RequestBody, loc string) {
	if b == nil || b.GoLow().IsReference() {
		return
	}
	l.walkContent(b.Content, loc)
}

func (l *linter) walkComponents(c *v3.Components) {
	for name, sp := range c.Schemas.FromOldest() {
		l.walkSchema(sp, finding.Pointer("#", "components", "schemas", name))
	}
	for name, p := range c.Parameters.FromOldest() {
		l.walkParam(p, finding.Pointer("#", "components", "parameters", name))
	}
	for name, r := range c.Responses.FromOldest() {
		l.walkResponse(r, finding.Pointer("#", "components", "responses", name))
	}
	for name, b := range c.RequestBodies.FromOldest() {
		l.walkRequestBody(b, finding.Pointer("#", "components", "requestBodies", name))
	}
}

// ---- merged schema view (allOf flattening) ----

type schemaRef struct {
	sp  *base.SchemaProxy
	loc string
}

type prop struct {
	name string
	schemaRef
}

// view is a schema with its allOf members merged in. oneOf/anyOf stay as alternatives.
type view struct {
	loc      string
	types    []string
	props    []prop
	required []string
	alts     []schemaRef
	items    *schemaRef
	addl     *schemaRef
}

func (l *linter) flatten(sp *base.SchemaProxy, loc string) view {
	return l.flattenRec(sp, loc, map[string]bool{}, 0)
}

func (l *linter) flattenRec(sp *base.SchemaProxy, loc string, seen map[string]bool, depth int) view {
	s, loc := deref(sp, loc)
	v := view{loc: loc}
	if s == nil || seen[loc] || depth > maxDepth {
		return v
	}
	seen[loc] = true
	v.types = s.Type
	v.required = s.Required
	for name, p := range s.Properties.FromOldest() {
		v.props = append(v.props, prop{name, schemaRef{p, finding.Pointer(loc, "properties", name)}})
	}
	for i, a := range s.OneOf {
		v.alts = append(v.alts, schemaRef{a, finding.Pointer(loc, "oneOf", strconv.Itoa(i))})
	}
	for i, a := range s.AnyOf {
		v.alts = append(v.alts, schemaRef{a, finding.Pointer(loc, "anyOf", strconv.Itoa(i))})
	}
	if s.Items != nil && s.Items.IsA() {
		v.items = &schemaRef{s.Items.A, finding.Pointer(loc, "items")}
	}
	if s.AdditionalProperties != nil && s.AdditionalProperties.IsA() {
		v.addl = &schemaRef{s.AdditionalProperties.A, finding.Pointer(loc, "additionalProperties")}
	}
	for i, m := range s.AllOf {
		sub := l.flattenRec(m, finding.Pointer(loc, "allOf", strconv.Itoa(i)), seen, depth+1)
		v.types = append(slices.Clone(v.types), sub.types...)
		v.props = append(v.props, sub.props...)
		v.required = append(slices.Clone(v.required), sub.required...)
		v.alts = append(v.alts, sub.alts...)
		v.items = cmpOr(v.items, sub.items)
		v.addl = cmpOr(v.addl, sub.addl)
	}
	return v
}

func cmpOr(a, b *schemaRef) *schemaRef {
	if a != nil {
		return a
	}
	return b
}

func (v view) isObject() bool {
	return slices.Contains(v.types, "object") || (len(v.types) == 0 && len(v.props) > 0)
}

// prop finds a property by name, also looking into oneOf/anyOf alternatives.
func (l *linter) prop(v view, name string) (prop, bool) {
	if i := slices.IndexFunc(v.props, func(p prop) bool { return p.name == name }); i >= 0 {
		return v.props[i], true
	}
	for _, a := range v.alts {
		if p, ok := l.prop(l.flatten(a.sp, a.loc), name); ok {
			return p, true
		}
	}
	return prop{}, false
}

// ---- hypermedia ----

func (l *linter) checkLinks(sp *base.SchemaProxy, loc string) {
	v := l.flatten(sp, loc)
	if !v.isObject() && v.addl == nil {
		l.must(jsonrules.RuleHypermedia, v.loc, "_links must be an object whose values are link objects")
		return
	}
	if v.addl != nil {
		l.checkLinkValue(v.addl.sp, v.addl.loc, 0)
	}
	for _, p := range v.props {
		l.checkLinkValue(p.sp, p.loc, 0)
	}
}

func (l *linter) checkLinkValue(sp *base.SchemaProxy, loc string, depth int) {
	v := l.flatten(sp, loc)
	switch {
	case depth > maxDepth:
	case len(v.alts) > 0 && len(v.props) == 0:
		for _, a := range l.nonNullAlts(v) {
			l.checkLinkValue(a.sp, a.loc, depth+1)
		}
	case slices.Contains(v.types, "array") && v.items != nil:
		l.checkLinkValue(v.items.sp, v.items.loc, depth+1)
	default:
		l.checkShape(v, "link", jsonrules.LinkFields, []string{"href"})
	}
}

// nonNullAlts returns v's oneOf/anyOf alternatives except the OpenAPI 3.1 `type: "null"` ones.
func (l *linter) nonNullAlts(v view) []schemaRef {
	return slices.DeleteFunc(slices.Clone(v.alts), func(a schemaRef) bool {
		return slices.Equal(l.flatten(a.sp, a.loc).types, []string{"null"})
	})
}

func (l *linter) checkShape(v view, what string, allowed, required []string) {
	for _, p := range v.props {
		if !slices.Contains(allowed, p.name) {
			l.must(jsonrules.RuleHypermedia, p.loc, "%s has unexpected property %q; allowed: %s", what, p.name, strings.Join(allowed, ", "))
		}
	}
	for _, r := range required {
		if !slices.Contains(v.required, r) {
			l.must(jsonrules.RuleHypermedia, v.loc, "%s must require property %q", what, r)
		}
	}
}

func (l *linter) checkActions(sp *base.SchemaProxy, loc string) {
	v := l.flatten(sp, loc)
	if !v.isObject() && v.addl == nil {
		l.must(jsonrules.RuleHypermedia, v.loc, "_actions must be an object whose values are action objects")
		return
	}
	if v.addl != nil {
		l.checkAction(v.addl.sp, v.addl.loc, 0)
	}
	for _, p := range v.props {
		l.checkAction(p.sp, p.loc, 0)
	}
}

func (l *linter) checkAction(sp *base.SchemaProxy, loc string, depth int) {
	v := l.flatten(sp, loc)
	if depth > maxDepth {
		return
	}
	if len(v.alts) > 0 && len(v.props) == 0 {
		for _, a := range l.nonNullAlts(v) {
			l.checkAction(a.sp, a.loc, depth+1)
		}
		return
	}
	l.checkShape(v, "action", jsonrules.ActionFields, []string{"href", "method"})
	if m, ok := l.prop(v, "method"); ok {
		l.checkEnum(m.schemaRef, "action method", jsonrules.ActionMethods)
	}
	if f, ok := l.prop(v, "fields"); ok {
		if items := l.flatten(f.sp, f.loc).items; items != nil {
			fv := l.flatten(items.sp, items.loc)
			l.checkShape(fv, "action field", jsonrules.FieldFields, nil)
			if t, ok := l.prop(fv, "type"); ok {
				l.checkEnum(t.schemaRef, "action field type", jsonrules.FieldTypes)
			}
		}
	}
}

func (l *linter) checkEnum(r schemaRef, what string, allowed []string) {
	s, loc := deref(r.sp, r.loc)
	if s == nil {
		return
	}
	for _, n := range s.Enum {
		if !slices.Contains(allowed, n.Value) {
			l.must(jsonrules.RuleHypermedia, finding.Pointer(loc, "enum"), "%s enum value %q is not allowed; expected one of %s", what, n.Value, strings.Join(allowed, ", "))
		}
	}
}

// ---- paths and operations ----

type mediaTypes = *orderedmap.Map[string, *v3.MediaType]

func mediaBase(mt string) string {
	if b, _, err := mime.ParseMediaType(mt); err == nil {
		return b
	}
	b, _, _ := strings.Cut(mt, ";")
	return strings.ToLower(strings.TrimSpace(b))
}

func isJSON(mt string) bool {
	b := mediaBase(mt)
	return b == "application/json" || strings.HasSuffix(b, "+json")
}

func (l *linter) checkPathItem(path string, item *v3.PathItem) {
	ploc := finding.Pointer("#", "paths", path)
	segs := strings.Split(strings.Trim(path, "/"), "/")
	if !versionSegmentRe.MatchString(segs[0]) && !serversCarryVersion(l.servers(item)) {
		l.must(RuleVersionPath, ploc, "path %q must start with a version segment such as /v1", path)
	}
	for i, seg := range segs {
		switch {
		case i == 0 && versionSegmentRe.MatchString(seg):
		case isLocaleTemplate(seg), localeLiteralRe.MatchString(seg):
			l.must(RuleLocale, ploc, "path segment %q looks like a locale; negotiate language with Accept-Language instead", seg)
		}
	}
	l.checkLocaleParams(item.Parameters, ploc)
	for i, p := range item.Parameters {
		l.walkParam(p, finding.Pointer(ploc, "parameters", strconv.Itoa(i)))
	}
	for method, op := range item.GetOperations().FromOldest() {
		if op != nil {
			l.checkOperation(strings.ToLower(method), item, op, finding.Pointer(ploc, strings.ToLower(method)))
		}
	}
}

// servers returns the servers applying to a path item: its own, else the document's.
func (l *linter) servers(item *v3.PathItem) []*v3.Server {
	if len(item.Servers) > 0 {
		return item.Servers
	}
	return l.docServers
}

var serverVarRe = regexp.MustCompile(`\{([^{}]+)\}`)

// serversCarryVersion reports whether every server URL, with variables replaced by their
// defaults, ends in a version segment such as /v1. No servers means no.
func serversCarryVersion(servers []*v3.Server) bool {
	for _, s := range servers {
		if s == nil {
			return false
		}
		raw := serverVarRe.ReplaceAllStringFunc(s.URL, func(m string) string {
			if v := s.Variables.GetOrZero(m[1 : len(m)-1]); v != nil {
				return v.Default
			}
			return m
		})
		u, err := url.Parse(raw)
		if err != nil {
			return false
		}
		segs := strings.Split(strings.Trim(u.Path, "/"), "/")
		if !versionSegmentRe.MatchString(segs[len(segs)-1]) {
			return false
		}
	}
	return len(servers) > 0
}

func isLocaleTemplate(seg string) bool {
	name, ok := strings.CutSuffix(strings.TrimPrefix(seg, "{"), "}")
	return ok && strings.HasPrefix(seg, "{") && slices.Contains(localeNames, strings.ToLower(name))
}

func (l *linter) checkLocaleParams(params []*v3.Parameter, parentLoc string) {
	for i, p := range params {
		if p != nil && p.In == "query" && slices.ContainsFunc(localeNames, func(n string) bool { return strings.EqualFold(n, p.Name) }) {
			l.must(RuleLocale, finding.Pointer(parentLoc, "parameters", strconv.Itoa(i)), "query parameter %q selects a locale; use the Accept-Language header instead", p.Name)
		}
	}
}

func (l *linter) checkOperation(method string, item *v3.PathItem, op *v3.Operation, loc string) {
	if slices.Contains([]string{"get", "put", "delete"}, method) && !slices.Contains(op.Tags, "idempotent") {
		l.must(RuleIdempotent, loc, "%s operation must have the tag \"idempotent\"", strings.ToUpper(method))
	}
	l.checkLocaleParams(op.Parameters, loc)
	for i, p := range op.Parameters {
		l.walkParam(p, finding.Pointer(loc, "parameters", strconv.Itoa(i)))
	}
	if op.RequestBody != nil {
		bloc := finding.Pointer(loc, "requestBody")
		l.walkRequestBody(op.RequestBody, bloc)
		for mt := range op.RequestBody.Content.FromOldest() {
			if mediaBase(mt) == "multipart/mixed" {
				l.must(RuleNoBatch, finding.Pointer(bloc, "content", mt), "batch requests (multipart/mixed) are not allowed; expose a dedicated resource instead")
			}
		}
	}
	if op.Responses == nil {
		return
	}
	rloc := finding.Pointer(loc, "responses")
	for code, r := range op.Responses.Codes.FromOldest() {
		l.checkResponse(code, r, finding.Pointer(rloc, code))
	}
	l.checkResponse("default", op.Responses.Default, finding.Pointer(rloc, "default"))
	l.checkPagination(item, op, loc)
}

func (l *linter) checkResponse(code string, r *v3.Response, loc string) {
	if r == nil {
		return
	}
	l.walkResponse(r, loc)
	switch {
	case strings.HasPrefix(code, "2"):
		for mt, m := range r.Content.FromOldest() {
			if !isJSON(mt) {
				continue
			}
			if m == nil || m.Schema == nil || !l.isEnvelope(m.Schema, finding.Pointer(loc, "content", mt, "schema")) {
				l.must(RuleEnvelope, loc, "success response must be an object with a \"data\" object property")
				return
			}
		}
	case strings.HasPrefix(code, "4"), strings.HasPrefix(code, "5"), code == "default":
		for mt := range r.Content.FromOldest() {
			if mediaBase(mt) != "application/problem+json" {
				l.must(RuleProblemJSON, loc, "error response must use application/problem+json, not %s", mt)
				return
			}
		}
	}
}

// isEnvelope reports whether the schema is an object with a "data" object property
// (for oneOf/anyOf: in every alternative).
func (l *linter) isEnvelope(sp *base.SchemaProxy, loc string) bool {
	v := l.flatten(sp, loc)
	if i := slices.IndexFunc(v.props, func(p prop) bool { return p.name == "data" }); i >= 0 && v.isObject() {
		return l.flatten(v.props[i].sp, v.props[i].loc).isObject()
	}
	return len(v.alts) > 0 && !slices.ContainsFunc(v.alts, func(a schemaRef) bool { return !l.isEnvelope(a.sp, a.loc) })
}

// ---- pagination ----

func (l *linter) checkPagination(item *v3.PathItem, op *v3.Operation, loc string) {
	query := map[string]bool{}
	for _, p := range append(slices.Clone(item.Parameters), op.Parameters...) {
		if p != nil && p.In == "query" {
			query[p.Name] = true
		}
	}
	if query["page"] && query["offset"] {
		l.must(jsonrules.RulePagination, loc, "operation must not combine \"page\" and \"offset\" query parameters; use page-based pagination only")
	}
	if !query["page"] {
		return
	}
	r := op.Responses.Codes.GetOrZero("200")
	if r == nil {
		return
	}
	rloc := finding.Pointer(loc, "responses", "200")
	for mt, m := range r.Content.FromOldest() {
		if !isJSON(mt) || m == nil || m.Schema == nil {
			continue
		}
		v := l.flatten(m.Schema, finding.Pointer(rloc, "content", mt, "schema"))
		p, ok := l.prop(v, "pagination")
		if !ok {
			l.must(jsonrules.RulePagination, rloc, "paginated response must have a top-level \"pagination\" property")
			return
		}
		pv := l.flatten(p.sp, p.loc)
		for _, name := range jsonrules.PaginationRequired {
			if _, ok := l.prop(pv, name); !ok {
				l.must(jsonrules.RulePagination, pv.loc, "pagination schema is missing property %q", name)
			}
		}
		return
	}
}

// ---- authentication ----

func (l *linter) checkBearer(doc *v3.Document) {
	const schemesLoc = "#/components/securitySchemes"
	var bearer []string
	formats := map[string]string{}
	hasJWT := false
	if doc.Components != nil {
		for name, s := range doc.Components.SecuritySchemes.FromOldest() {
			if s != nil && s.Type == "http" && strings.EqualFold(s.Scheme, "bearer") {
				bearer = append(bearer, name)
				formats[name] = s.BearerFormat
				hasJWT = hasJWT || s.BearerFormat == "JWT"
			}
		}
	}
	for _, name := range bearer {
		loc := finding.Pointer(schemesLoc, name, "bearerFormat")
		switch {
		case formats[name] == "JWT", formats[name] == "opaque" && hasJWT:
		case formats[name] == "opaque":
			l.add(RuleBearer, finding.Should, loc, "bearer scheme %q: opaque bearer tokens are allowed only for personal access tokens next to a JWT scheme", name)
		default:
			l.add(RuleBearer, finding.Should, loc, "bearer scheme %q should declare bearerFormat: JWT", name)
		}
	}
	if len(bearer) == 0 {
		l.must(RuleBearer, schemesLoc, "components.securitySchemes must define an http bearer scheme (scheme: bearer, bearerFormat: JWT)")
		return
	}
	topOK := securityReferences(doc.Security, bearer)
	operations := 0
	if doc.Paths != nil {
		for path, item := range doc.Paths.PathItems.FromOldest() {
			for method, op := range item.GetOperations().FromOldest() {
				operations++
				// A nil slice means "inherit"; a non-nil empty one is an explicit public operation.
				effectiveOK := topOK
				if op != nil && op.Security != nil {
					effectiveOK = len(op.Security) == 0 || securityReferences(op.Security, bearer)
				}
				if !effectiveOK {
					l.must(RuleBearer, finding.Pointer("#", "paths", path, strings.ToLower(method)), "operation security must reference the bearer scheme %q", bearer[0])
				}
			}
		}
	}
	if operations == 0 && !topOK {
		l.must(RuleBearer, "#/security", "top-level security must reference the bearer scheme %q", bearer[0])
	}
}

func securityReferences(reqs []*base.SecurityRequirement, names []string) bool {
	for _, req := range reqs {
		for name := range req.Requirements.FromOldest() {
			if slices.Contains(names, name) {
				return true
			}
		}
	}
	return false
}
