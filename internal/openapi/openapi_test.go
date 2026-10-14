package openapi

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/floriankraemer/http-api-spec/validator/internal/finding"
)

// header is a valid, finding-free prologue; tests append paths and components.
const header = `openapi: "%s"
info: {title: t, version: "1"}
security:
  - bearerAuth: []
`

const bearer = `  securitySchemes:
    bearerAuth: {type: http, scheme: bearer, bearerFormat: JWT}
`

func doc(version, paths, schemas string) string {
	return strings.Replace(header, "%s", version, 1) + "paths:\n" + paths + "components:\n" + bearer + "  schemas:\n" + schemas
}

const okOp = `  /v1/things:
    get:
      tags: [idempotent]
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Resp'}
`

const okResp = `    Resp:
      type: object
      properties:
        data: {type: object}
`

func lintDoc(t *testing.T, src string) []finding.Finding {
	t.Helper()
	fs, err := Lint([]byte(src))
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	return fs
}

func rules(fs []finding.Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.RuleID)
	}
	return out
}

func TestBaselineIsClean(t *testing.T) {
	for _, v := range []string{"3.0.3", "3.1.0"} {
		if fs := lintDoc(t, doc(v, okOp, okResp)); len(fs) != 0 {
			t.Errorf("%s: unexpected findings %+v", v, fs)
		}
	}
}

func TestSchemaRules(t *testing.T) {
	tests := []struct {
		name, schemas string
		rule, loc     string
		line          int
	}{
		{"snake case", okResp + "    Order:\n      type: object\n      properties:\n        customer_id: {type: string}\n",
			"naming-camel-case", "#/components/schemas/Order/properties/customer_id", 26},
		{"underscore only reports underscore", okResp + "    Order:\n      type: object\n      properties:\n        _secret: {type: string}\n",
			"no-underscore-fields", "#/components/schemas/Order/properties/_secret", 26},
		{"nested in items and allOf", okResp + "    Order:\n      allOf:\n        - type: array\n          items:\n            type: object\n            properties:\n              Bad_Name: {type: string}\n",
			"naming-camel-case", "#/components/schemas/Order/allOf/0/items/properties/Bad_Name", 29},
		{"additionalProperties and oneOf", okResp + "    Order:\n      type: object\n      additionalProperties:\n        oneOf:\n          - type: object\n            properties:\n              bad_one: {type: string}\n",
			"naming-camel-case", "#/components/schemas/Order/additionalProperties/oneOf/0/properties/bad_one", 29},
	}
	for _, v := range []string{"3.0.3", "3.1.0"} {
		for _, tc := range tests {
			t.Run(v+"/"+tc.name, func(t *testing.T) {
				fs := lintDoc(t, doc(v, okOp, tc.schemas))
				if len(fs) != 1 {
					t.Fatalf("want 1 finding, got %+v", fs)
				}
				f := fs[0]
				if f.RuleID != tc.rule || f.Location != tc.loc || f.Level != finding.Must || f.Line != tc.line || f.Message == "" {
					t.Errorf("got %+v, want rule=%s loc=%s line=%d", f, tc.rule, tc.loc, tc.line)
				}
			})
		}
	}
}

func TestControlFieldsExempt(t *testing.T) {
	s := okResp + "    Order:\n      type: object\n      properties:\n        _links: {type: object, additionalProperties: {$ref: '#/components/schemas/L'}}\n        _actions: {type: object, additionalProperties: {$ref: '#/components/schemas/A'}}\n" +
		"    L: {type: object, required: [href], properties: {href: {type: string}}}\n" +
		"    A: {type: object, required: [href, method], properties: {href: {type: string}, method: {type: string, enum: [POST]}}}\n"
	if fs := lintDoc(t, doc("3.1.0", okOp, s)); len(fs) != 0 {
		t.Errorf("unexpected %+v", fs)
	}
}

func TestRefViolationReportedOnce(t *testing.T) {
	// Customer is referenced from three places; the violation lives in the component only.
	s := "    Resp:\n      type: object\n      properties:\n        data:\n          type: object\n          properties:\n            a: {$ref: '#/components/schemas/Customer'}\n            b: {$ref: '#/components/schemas/Customer'}\n            c:\n              type: array\n              items: {$ref: '#/components/schemas/Customer'}\n" +
		"    Customer:\n      type: object\n      properties:\n        first_name: {type: string}\n"
	fs := lintDoc(t, doc("3.1.0", okOp, s))
	if len(fs) != 1 || fs[0].Location != "#/components/schemas/Customer/properties/first_name" || fs[0].Line != 33 {
		t.Fatalf("got %+v", fs)
	}
}

func TestCircularRefTerminates(t *testing.T) {
	s := "    Resp:\n      type: object\n      properties:\n        data: {$ref: '#/components/schemas/Node'}\n" +
		"    Node:\n      type: object\n      properties:\n        child: {$ref: '#/components/schemas/Node'}\n        bad_name: {type: string}\n        kids:\n          type: array\n          items: {$ref: '#/components/schemas/Node'}\n"
	for _, v := range []string{"3.0.3", "3.1.0"} {
		fs := lintDoc(t, doc(v, okOp, s))
		if len(fs) != 1 || fs[0].RuleID != "naming-camel-case" {
			t.Errorf("%s: got %+v", v, fs)
		}
	}
}

func TestCircularAllOfTerminates(t *testing.T) {
	s := "    Resp:\n      allOf:\n        - $ref: '#/components/schemas/Resp'\n"
	fs := lintDoc(t, doc("3.1.0", okOp, s))
	if len(fs) != 1 || fs[0].RuleID != "success-data-envelope" {
		t.Errorf("got %+v", fs)
	}
}

// pathRule runs a paths-only mutation (schemas stay valid) and returns rule IDs.
func pathRule(t *testing.T, version, paths string) []finding.Finding {
	return lintDoc(t, doc(version, paths, okResp))
}

func op(path, method, extra string) string {
	return "  " + path + ":\n    " + method + ":\n" + extra
}

const okGetBody = `      tags: [idempotent]
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema: {$ref: '#/components/schemas/Resp'}
`

func TestPathRules(t *testing.T) {
	tests := []struct {
		name, paths string
		rules       []string
		loc         string
	}{
		{"no version", op("/orders", "get", okGetBody), []string{"version-path-prefix"}, "#/paths/~1orders"},
		{"bad version", op("/api/orders", "get", okGetBody), []string{"version-path-prefix"}, "#/paths/~1api~1orders"},
		{"locale template", op("/v1/{lang}/orders", "get", okGetBody), []string{"locale-not-in-url"}, "#/paths/~1v1~1{lang}~1orders"},
		{"locale literal", op("/v1/de-CH/orders", "get", okGetBody), []string{"locale-not-in-url"}, "#/paths/~1v1~1de-CH~1orders"},
		// Bare two-letter segments are not flagged: /v1/me or /v1/ai are common resource names.
		{"two-letter segment me", op("/v1/me", "get", okGetBody), nil, ""},
		{"two-letter segment nested", op("/v1/me/orders", "get", okGetBody), nil, ""},
		{"locale query", op("/v1/things", "get", "      parameters:\n        - {name: locale, in: query, schema: {type: string}}\n"+okGetBody), []string{"locale-not-in-url"}, "#/paths/~1v1~1things/get/parameters/0"},
		{"path level locale query", "  /v1/things:\n    parameters:\n      - {name: lang, in: query, schema: {type: string}}\n    get:\n" + okGetBody, []string{"locale-not-in-url"}, "#/paths/~1v1~1things/parameters/0"},
		{"get missing tag", op("/v1/things", "get", strings.Replace(okGetBody, "      tags: [idempotent]\n", "", 1)), []string{"idempotent-tag"}, "#/paths/~1v1~1things/get"},
		{"put missing tag", op("/v1/things", "put", strings.Replace(okGetBody, "      tags: [idempotent]\n", "      tags: [other]\n", 1)), []string{"idempotent-tag"}, "#/paths/~1v1~1things/put"},
		{"delete missing tag", op("/v1/things", "delete", "      responses:\n        '204': {description: gone}\n"), []string{"idempotent-tag"}, "#/paths/~1v1~1things/delete"},
		{"post needs no tag", op("/v1/things", "post", "      responses:\n        '204': {description: ok}\n"), nil, ""},
		{"batch", op("/v1/batch", "post", "      requestBody:\n        content:\n          multipart/mixed:\n            schema: {type: string}\n      responses:\n        '204': {description: ok}\n"), []string{"no-batch-requests"}, "#/paths/~1v1~1batch/post/requestBody/content/multipart~1mixed"},
		{"error not problem json", op("/v1/things", "get", okGetBody+"        '404':\n          description: nf\n          content:\n            application/json:\n              schema: {$ref: '#/components/schemas/Resp'}\n"), []string{"error-problem-json"}, "#/paths/~1v1~1things/get/responses/404"},
		{"default not problem json", op("/v1/things", "get", okGetBody+"        default:\n          description: err\n          content:\n            text/plain:\n              schema: {type: string}\n"), []string{"error-problem-json"}, "#/paths/~1v1~1things/get/responses/default"},
		{"problem json ok", op("/v1/things", "get", okGetBody+"        '500':\n          description: err\n          content:\n            application/problem+json:\n              schema: {type: object}\n"), nil, ""},
		{"error without content ok", op("/v1/things", "get", okGetBody+"        '404': {description: nf}\n"), nil, ""},
		{"envelope missing data", op("/v1/things", "get", strings.Replace(okGetBody, "{$ref: '#/components/schemas/Resp'}", "{type: object, properties: {orders: {type: array, items: {type: string}}}}", 1)), []string{"success-data-envelope"}, "#/paths/~1v1~1things/get/responses/200"},
		{"envelope data not object", op("/v1/things", "get", strings.Replace(okGetBody, "{$ref: '#/components/schemas/Resp'}", "{type: object, properties: {data: {type: array, items: {type: string}}}}", 1)), []string{"success-data-envelope"}, "#/paths/~1v1~1things/get/responses/200"},
		{"envelope via allOf ok", op("/v1/things", "get", strings.Replace(okGetBody, "{$ref: '#/components/schemas/Resp'}", "{allOf: [{type: object}, {$ref: '#/components/schemas/Resp'}]}", 1)), nil, ""},
		{"envelope vendor json", op("/v1/things", "get", strings.Replace(strings.Replace(okGetBody, "application/json", "application/vnd.x+json", 1), "{$ref: '#/components/schemas/Resp'}", "{type: object}", 1)), []string{"success-data-envelope"}, "#/paths/~1v1~1things/get/responses/200"},
		{"non json success ignored", op("/v1/things", "get", strings.Replace(okGetBody, "application/json", "text/csv", 1)), nil, ""},
		{"page without pagination", op("/v1/things", "get", "      parameters:\n        - {name: page, in: query, schema: {type: integer}}\n"+okGetBody), []string{"pagination-shape"}, "#/paths/~1v1~1things/get/responses/200"},
	}
	for _, v := range []string{"3.0.3", "3.1.0"} {
		for _, tc := range tests {
			t.Run(v+"/"+tc.name, func(t *testing.T) {
				fs := pathRule(t, v, tc.paths)
				if got := rules(fs); strings.Join(got, ",") != strings.Join(tc.rules, ",") {
					t.Fatalf("rules = %v, want %v (%+v)", got, tc.rules, fs)
				}
				if len(fs) > 0 && fs[0].Location != tc.loc {
					t.Errorf("location = %s, want %s", fs[0].Location, tc.loc)
				}
				for _, f := range fs {
					if f.Line == 0 {
						t.Errorf("missing line: %+v", f)
					}
				}
			})
		}
	}
}

func TestPageAndOffsetTogether(t *testing.T) {
	s := "    Resp:\n      type: object\n      properties:\n        data: {type: object}\n        pagination: {$ref: '#/components/schemas/P'}\n" +
		"    P:\n      type: object\n      properties:\n        currentPage: {type: integer}\n        totalPages: {type: integer}\n        perPage: {type: integer}\n        totalItems: {type: integer}\n"
	paths := op("/v1/things", "get", "      parameters:\n        - {name: page, in: query, schema: {type: integer}}\n        - {name: offset, in: query, schema: {type: integer}}\n"+okGetBody)
	fs := lintDoc(t, doc("3.1.0", paths, s))
	if len(fs) != 1 || fs[0].RuleID != "pagination-shape" || fs[0].Location != "#/paths/~1v1~1things/get" {
		t.Fatalf("got %+v", fs)
	}
}

func TestPaginationSchemaMissingMember(t *testing.T) {
	s := "    Resp:\n      type: object\n      properties:\n        data: {type: object}\n        pagination: {$ref: '#/components/schemas/P'}\n" +
		"    P:\n      type: object\n      properties:\n        currentPage: {type: integer}\n        totalPages: {type: integer}\n        perPage: {type: integer}\n"
	paths := op("/v1/things", "get", "      parameters:\n        - {name: page, in: query, schema: {type: integer}}\n"+okGetBody)
	fs := lintDoc(t, doc("3.1.0", paths, s))
	if len(fs) != 1 || fs[0].RuleID != "pagination-shape" || fs[0].Location != "#/components/schemas/P" || !strings.Contains(fs[0].Message, "totalItems") {
		t.Fatalf("got %+v", fs)
	}
}

func TestBearerRules(t *testing.T) {
	base := func(security, schemes string) string {
		return "openapi: 3.1.0\ninfo: {title: t, version: '1'}\n" + security + "paths: {}\ncomponents:\n" + schemes
	}
	tests := []struct {
		name, src, rule string
		level           finding.Level
		loc             string
	}{
		{"no scheme", base("security:\n  - k: []\n", "  securitySchemes:\n    k: {type: apiKey, name: x, in: header}\n"), "bearer-jwt-auth", finding.Must, "#/components/securitySchemes"},
		{"no components", base("", ""), "bearer-jwt-auth", finding.Must, "#/components/securitySchemes"},
		{"not referenced", base("", "  securitySchemes:\n    b: {type: http, scheme: bearer, bearerFormat: JWT}\n"), "bearer-jwt-auth", finding.Must, "#/security"},
		{"references other", base("security:\n  - k: []\n", "  securitySchemes:\n    b: {type: http, scheme: bearer, bearerFormat: JWT}\n    k: {type: apiKey, name: x, in: header}\n"), "bearer-jwt-auth", finding.Must, "#/security"},
		{"not JWT", base("security:\n  - b: []\n", "  securitySchemes:\n    b: {type: http, scheme: bearer}\n"), "bearer-jwt-auth", finding.Should, "#/components/securitySchemes/b/bearerFormat"},
		{"opaque only", base("security:\n  - p: []\n", "  securitySchemes:\n    p: {type: http, scheme: bearer, bearerFormat: opaque}\n"), "bearer-jwt-auth", finding.Should, "#/components/securitySchemes/p/bearerFormat"},
		{"other format next to JWT", base("security:\n  - b: []\n", "  securitySchemes:\n    b: {type: http, scheme: bearer, bearerFormat: JWT}\n    o: {type: http, scheme: bearer, bearerFormat: PASETO}\n"), "bearer-jwt-auth", finding.Should, "#/components/securitySchemes/o/bearerFormat"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := lintDoc(t, tc.src)
			if len(fs) != 1 || fs[0].RuleID != tc.rule || fs[0].Level != tc.level || fs[0].Location != tc.loc || fs[0].Line == 0 {
				t.Fatalf("got %+v", fs)
			}
		})
	}
}

func TestBearerOpaqueNextToJWT(t *testing.T) {
	schemes := "  securitySchemes:\n    b: {type: http, scheme: bearer, bearerFormat: JWT}\n    p: {type: http, scheme: bearer, bearerFormat: opaque}\n"
	for name, security := range map[string]string{"jwt referenced": "security:\n  - b: []\n", "only opaque referenced": "security:\n  - p: []\n"} {
		t.Run(name, func(t *testing.T) {
			src := "openapi: 3.1.0\ninfo: {title: t, version: '1'}\n" + security + "paths: {}\ncomponents:\n" + schemes
			if fs := lintDoc(t, src); len(fs) != 0 {
				t.Fatalf("got %+v", fs)
			}
		})
	}
}

func hm(schemas string) string {
	return "    Resp:\n      type: object\n      properties:\n        data: {type: object}\n" + schemas
}

func TestHypermediaRules(t *testing.T) {
	linkOK := "    Link: {type: object, required: [href], properties: {href: {type: string}, title: {type: string}}}\n"
	tests := []struct {
		name, schemas, loc, msg string
	}{
		{"link extra property",
			"        _links: {type: object, additionalProperties: {$ref: '#/components/schemas/Link'}}\n    Link: {type: object, required: [href], properties: {href: {type: string}, rel: {type: string}}}\n",
			"#/components/schemas/Link/properties/rel", "rel"},
		{"link missing href",
			"        _links: {type: object, additionalProperties: {$ref: '#/components/schemas/Link'}}\n    Link: {type: object, properties: {href: {type: string}}}\n",
			"#/components/schemas/Link", "href"},
		{"named link properties",
			"        _links:\n          type: object\n          properties:\n            self: {type: object, properties: {url: {type: string}}, required: [href]}\n",
			"#/components/schemas/Resp/properties/_links/properties/self/properties/url", "url"},
		{"link array oneOf",
			"        _links: {type: object, additionalProperties: {oneOf: [{$ref: '#/components/schemas/Link'}, {type: array, items: {type: object, properties: {bad: {type: string}}, required: [href]}}]}}\n" + linkOK,
			"#/components/schemas/Resp/properties/_links/additionalProperties/oneOf/1/items/properties/bad", "bad"},
		{"links not object",
			"        _links: {type: array, items: {type: string}}\n",
			"#/components/schemas/Resp/properties/_links", "object"},
		{"action GET method",
			"        _actions: {type: object, additionalProperties: {$ref: '#/components/schemas/Act'}}\n    Act: {type: object, required: [href, method], properties: {href: {type: string}, method: {type: string, enum: [GET, POST]}}}\n",
			"#/components/schemas/Act/properties/method/enum", "GET"},
		{"action missing method",
			"        _actions: {type: object, additionalProperties: {$ref: '#/components/schemas/Act'}}\n    Act: {type: object, required: [href], properties: {href: {type: string}, method: {type: string}}}\n",
			"#/components/schemas/Act", "method"},
		{"action extra property",
			"        _actions: {type: object, additionalProperties: {$ref: '#/components/schemas/Act'}}\n    Act: {type: object, required: [href, method], properties: {href: {type: string}, method: {type: string}, name: {type: string}}}\n",
			"#/components/schemas/Act/properties/name", "name"},
		{"field extra property and type",
			"        _actions: {type: object, additionalProperties: {$ref: '#/components/schemas/Act'}}\n    Act: {type: object, required: [href, method], properties: {href: {type: string}, method: {type: string}, fields: {type: array, items: {type: object, properties: {name: {type: string}, type: {type: string, enum: [string, date]}}}}}}\n",
			"#/components/schemas/Act/properties/fields/items/properties/type/enum", "date"},
		{"field extra",
			"        _actions: {type: object, additionalProperties: {$ref: '#/components/schemas/Act'}}\n    Act: {type: object, required: [href, method], properties: {href: {type: string}, method: {type: string}, fields: {type: array, items: {type: object, properties: {name: {type: string}, label: {type: string}}}}}}\n",
			"#/components/schemas/Act/properties/fields/items/properties/label", "label"},
	}
	for _, v := range []string{"3.0.3", "3.1.0"} {
		for _, tc := range tests {
			t.Run(v+"/"+tc.name, func(t *testing.T) {
				fs := lintDoc(t, doc(v, okOp, hm(tc.schemas)))
				if len(fs) != 1 || fs[0].RuleID != "hypermedia-shape" || fs[0].Location != tc.loc || !strings.Contains(fs[0].Message, tc.msg) || fs[0].Line == 0 {
					t.Fatalf("got %+v, want loc %s msg %q", fs, tc.loc, tc.msg)
				}
			})
		}
	}
}

func TestHypermediaViolationInSharedRefReportedOnce(t *testing.T) {
	s := "    Resp:\n      type: object\n      properties:\n        data: {type: object}\n        _links: {$ref: '#/components/schemas/Links'}\n" +
		"    Other:\n      type: object\n      properties:\n        _links: {$ref: '#/components/schemas/Links'}\n" +
		"    Links: {type: object, additionalProperties: {type: object, properties: {rel: {type: string}}, required: [href]}}\n"
	fs := lintDoc(t, doc("3.1.0", okOp, s))
	if len(fs) != 1 {
		t.Fatalf("got %+v", fs)
	}
}

func TestInvalidInput(t *testing.T) {
	for name, src := range map[string]string{
		"garbage": "{{{ not yaml",
		"empty":   "",
		"swagger": "swagger: '2.0'\ninfo: {title: t, version: '1'}\npaths: {}\n",
		"not api": "foo: bar\n",
	} {
		if _, err := Lint([]byte(src)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestResultIsSorted(t *testing.T) {
	s := okResp + "    A:\n      type: object\n      properties:\n        b_b: {type: string}\n        c_c: {type: string}\n"
	fs := lintDoc(t, doc("3.1.0", op("/orders", "get", okGetBody), s))
	for i := 1; i < len(fs); i++ {
		if fs[i-1].Line > fs[i].Line {
			t.Errorf("unsorted: %+v", fs)
		}
	}
}

func TestFixtures(t *testing.T) {
	files, err := filepath.Glob("../../testdata/openapi/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".yaml")
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			fs, err := Lint(data)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.HasPrefix(name, "good"):
				if len(fs) != 0 {
					t.Errorf("want no findings, got %+v", fs)
				}
			case strings.HasPrefix(name, "bad-"):
				rule := strings.TrimPrefix(name, "bad-")
				if len(fs) == 0 {
					t.Errorf("want findings for %s", rule)
				}
				for _, f := range fs {
					if f.RuleID != rule {
						t.Errorf("want only %s, got %+v", rule, f)
					}
				}
			default:
				t.Fatalf("fixture %s does not follow the good*/bad-<rule> convention", name)
			}
		})
	}
}

func bearerDoc(top, paths, nullable string) string {
	return "openapi: 3.1.0\ninfo: {title: t, version: '1'}\n" + top + "paths:\n" + paths +
		"components:\n  securitySchemes:\n    b: {type: http, scheme: bearer, bearerFormat: JWT}\n    k: {type: apiKey, name: x, in: header}\n" +
		"  schemas:\n    Resp: {type: object, properties: {data: {type: object}}}\n" + nullable
}

func secOp(path, security string) string {
	return "  " + path + ":\n    get:\n      tags: [idempotent]\n" + security +
		"      responses:\n        '200':\n          description: ok\n          content:\n            application/json:\n              schema: {$ref: '#/components/schemas/Resp'}\n"
}

func TestBearerPerOperationSecurity(t *testing.T) {
	tests := []struct {
		name, top, paths string
		locs             []string
	}{
		{"per-operation only", "", secOp("/v1/a", "      security: [{b: []}]\n") + secOp("/v1/b", "      security: [{b: []}]\n"), nil},
		{"mix reports the uncovered operation", "", secOp("/v1/a", "      security: [{b: []}]\n") + secOp("/v1/b", ""), []string{"#/paths/~1v1~1b/get"}},
		{"operation overrides bearer top-level with other scheme", "security: [{b: []}]\n", secOp("/v1/a", "      security: [{k: []}]\n"), []string{"#/paths/~1v1~1a/get"}},
		{"operation inherits top-level with other scheme", "security: [{k: []}]\n", secOp("/v1/a", ""), []string{"#/paths/~1v1~1a/get"}},
		{"explicit empty security is public", "", secOp("/v1/a", "      security: [{b: []}]\n") + secOp("/v1/b", "      security: []\n"), nil},
		{"top-level covers inheriting operations", "security: [{b: []}]\n", secOp("/v1/a", ""), nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := lintDoc(t, bearerDoc(tc.top, tc.paths, ""))
			var got []string
			for _, f := range fs {
				if f.RuleID != "bearer-jwt-auth" {
					t.Fatalf("unexpected finding %+v", f)
				}
				got = append(got, f.Location)
			}
			if !slices.Equal(got, tc.locs) {
				t.Fatalf("locations = %v, want %v (%+v)", got, tc.locs, fs)
			}
		})
	}
}

func TestNullableLinkAlternatives(t *testing.T) {
	link := "    Link: {type: object, required: [href], properties: {href: {type: string}}}\n"
	act := "    Act: {type: object, required: [href, method], properties: {href: {type: string}, method: {type: string}}}\n"
	tests := []struct{ name, version, schemas string }{
		{"3.1 oneOf null link", "3.1.0", "        _links: {type: object, additionalProperties: {oneOf: [{$ref: '#/components/schemas/Link'}, {type: 'null'}]}}\n" + link},
		{"3.1 anyOf null action", "3.1.0", "        _actions: {type: object, additionalProperties: {anyOf: [{$ref: '#/components/schemas/Act'}, {type: 'null'}]}}\n" + act},
		{"3.1 null in type array", "3.1.0", "        _links: {type: object, properties: {self: {oneOf: [{$ref: '#/components/schemas/Link'}, {type: [null]}]}}}\n" + link},
		{"3.0 nullable link", "3.0.3", "        _links: {type: object, additionalProperties: {nullable: true, allOf: [{$ref: '#/components/schemas/Link'}]}}\n" + link},
		{"3.0 nullable action", "3.0.3", "        _actions: {type: object, additionalProperties: {nullable: true, allOf: [{$ref: '#/components/schemas/Act'}]}}\n" + act},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if fs := lintDoc(t, doc(tc.version, okOp, hm(tc.schemas))); len(fs) != 0 {
				t.Fatalf("got %+v", fs)
			}
		})
	}
}

func TestPathLevelParameterSchemasWalked(t *testing.T) {
	paths := "  /v1/things:\n    parameters:\n      - name: filter\n        in: query\n        schema: {type: object, properties: {bad_name: {type: string}}}\n" +
		"    get:\n" + okGetBody
	fs := lintDoc(t, doc("3.1.0", paths, okResp))
	want := "#/paths/~1v1~1things/parameters/0/schema/properties/bad_name"
	if len(fs) != 1 || fs[0].RuleID != "naming-camel-case" || fs[0].Location != want {
		t.Fatalf("got %+v, want one camelCase finding at %s", fs, want)
	}
}

func TestVersionInServerURL(t *testing.T) {
	withServers := func(top, pathItemServers, path string) string {
		src := doc("3.1.0", "  "+path+":\n"+pathItemServers+"    get:\n"+okGetBody, okResp)
		return strings.Replace(src, "paths:\n", top+"paths:\n", 1)
	}
	tests := []struct {
		name, src string
		rules     []string
	}{
		{"absolute server version", withServers("servers: [{url: 'https://x/v1'}]\n", "", "/orders"), nil},
		{"trailing slash", withServers("servers: [{url: 'https://x/v1/'}]\n", "", "/orders"), nil},
		{"relative server version", withServers("servers: [{url: /v1}]\n", "", "/orders"), nil},
		{"nested base path", withServers("servers: [{url: 'https://x/api/v3'}]\n", "", "/orders"), nil},
		{"variable default", withServers("servers: [{url: 'https://x/{basePath}', variables: {basePath: {default: v2}}}]\n", "", "/orders"), nil},
		{"server without version", withServers("servers: [{url: 'https://x/api'}]\n", "", "/orders"), []string{"version-path-prefix"}},
		{"host only server", withServers("servers: [{url: 'https://x'}]\n", "", "/orders"), []string{"version-path-prefix"}},
		{"mixed servers", withServers("servers: [{url: 'https://x/v1'}, {url: 'https://y'}]\n", "", "/orders"), []string{"version-path-prefix"}},
		{"all servers versioned", withServers("servers: [{url: 'https://x/v1'}, {url: 'https://y/v2'}]\n", "", "/orders"), nil},
		{"path-item servers override top-level", withServers("servers: [{url: 'https://x'}]\n", "    servers: [{url: 'https://x/v1'}]\n", "/orders"), nil},
		{"path-item servers without version", withServers("servers: [{url: 'https://x/v1'}]\n", "    servers: [{url: 'https://x'}]\n", "/orders"), []string{"version-path-prefix"}},
		{"path carries version despite bad server", withServers("servers: [{url: 'https://x'}]\n", "", "/v1/orders"), nil},
		{"locale template with server version", withServers("servers: [{url: /v1}]\n", "", "/{lang}/orders"), []string{"locale-not-in-url"}},
		{"locale literal with server version", withServers("servers: [{url: /v1}]\n", "", "/de-CH/orders"), []string{"locale-not-in-url"}},
		{"locale after path version", withServers("", "", "/v1/de-CH/orders"), []string{"locale-not-in-url"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := lintDoc(t, tc.src)
			if got := rules(fs); !slices.Equal(got, tc.rules) {
				t.Fatalf("rules = %v, want %v (%+v)", got, tc.rules, fs)
			}
		})
	}
}

func TestMapKeysAreNotFieldNames(t *testing.T) {
	schemas := okResp + `    Names:
      type: object
      properties:
        labels:
          type: object
          additionalProperties: {type: string}
          example: {en-US: Hello, de_CH: Hallo}
        byPattern:
          type: object
          patternProperties:
            '^[a-z_-]+$': {type: string}
`
	for _, v := range []string{"3.0.3", "3.1.0"} {
		if fs := lintDoc(t, doc(v, okOp, schemas)); len(fs) != 0 {
			t.Errorf("%s: got %+v", v, fs)
		}
	}
}

func TestUndefinedServerVariableDoesNotPanic(t *testing.T) {
	src := strings.Replace(doc("3.1.0", op("/orders", "get", okGetBody), okResp), "paths:\n", "servers: [{url: 'https://x/{nope}'}]\npaths:\n", 1)
	if got := rules(lintDoc(t, src)); !slices.Equal(got, []string{"version-path-prefix"}) {
		t.Fatalf("rules = %v", got)
	}
}
