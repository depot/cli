package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// jsonValue is a JSON value and the position of its source text. The JSON
// rewrite changes the source text in place: encoding the value again would
// drop repeated object keys, which HCL JSON uses to repeat blocks.
type jsonValue struct {
	start, end int
	isString   bool
	text       string
	isObject   bool
	members    []jsonMember
	elements   []*jsonValue
}

type jsonMember struct {
	key, value *jsonValue
}

func parseJSON(data []byte) (*jsonValue, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := parseJSONValue(decoder, data)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("data after the JSON value")
	}
	if !value.isObject {
		return nil, errors.New("the JSON value is not an object")
	}
	return value, nil
}

func parseJSONValue(decoder *json.Decoder, data []byte) (*jsonValue, error) {
	value := &jsonValue{start: int(decoder.InputOffset())}
	for value.start < len(data) && strings.IndexByte(" \t\r\n,:", data[value.start]) >= 0 {
		value.start++
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch token := token.(type) {
	case string:
		value.isString = true
		value.text = token
	case json.Delim:
		value.isObject = token == '{'
		for decoder.More() {
			element, err := parseJSONValue(decoder, data)
			if err != nil {
				return nil, err
			}
			if !value.isObject {
				value.elements = append(value.elements, element)
				continue
			}
			member, err := parseJSONValue(decoder, data)
			if err != nil {
				return nil, err
			}
			value.members = append(value.members, jsonMember{key: element, value: member})
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
	}
	value.end = int(decoder.InputOffset())
	return value, nil
}

// objects returns the value if it is an object, or the objects in the value
// if it is an array. HCL JSON gives blocks in either form.
func (v *jsonValue) objects() []*jsonValue {
	if v.isObject {
		return []*jsonValue{v}
	}
	var out []*jsonValue
	for _, element := range v.elements {
		if element.isObject {
			out = append(out, element)
		}
	}
	return out
}

func (v *jsonValue) values(key string) []*jsonValue {
	var out []*jsonValue
	for _, m := range v.members {
		if m.key.text == key {
			out = append(out, m.value)
		}
	}
	return out
}

type jsonBlock struct {
	label string
	body  *jsonValue
}

// blocks returns the blocks of a type in a JSON definition.
func (v *jsonValue) blocks(blockType string) []jsonBlock {
	var out []jsonBlock
	for _, value := range v.values(blockType) {
		for _, labels := range value.objects() {
			for _, m := range labels.members {
				for _, body := range m.value.objects() {
					out = append(out, jsonBlock{label: m.key.text, body: body})
				}
			}
		}
	}
	return out
}

func (v *jsonValue) isNull(data []byte) bool {
	return !v.isString && !v.isObject && string(data[v.start:v.end]) == "null"
}

func (v *jsonValue) contains(other *jsonValue) bool {
	return other.start >= v.start && other.end <= v.end
}

// visitTemplates calls visit for each string that bake reads as a template,
// which is every string except those in which target refers to a variable:
// the result of a function with a parameter named target, and the
// attributes of a matrix target with a variable named target.
func (v *jsonValue) visitTemplates(visit func(*jsonValue)) {
	var scopes []*jsonValue
	for _, function := range v.blocks("function") {
		if hasJSONTargetParameter(function.body) {
			scopes = append(scopes, function.body.values("result")...)
		}
	}
	for _, target := range v.blocks("target") {
		if slices.ContainsFunc(target.body.values("matrix"), func(matrix *jsonValue) bool { return len(matrix.values("target")) > 0 }) {
			for _, m := range target.body.members {
				if m.key.text != "matrix" {
					scopes = append(scopes, m.value)
				}
			}
		}
	}
	v.visitStrings(func(s *jsonValue) {
		if !slices.ContainsFunc(scopes, func(scope *jsonValue) bool { return scope.contains(s) }) {
			visit(s)
		}
	})
}

func (v *jsonValue) visitStrings(visit func(*jsonValue)) {
	if v.isString {
		visit(v)
	}
	for _, m := range v.members {
		m.key.visitStrings(visit)
		m.value.visitStrings(visit)
	}
	for _, element := range v.elements {
		element.visitStrings(visit)
	}
}

func hasJSONTargetParameter(function *jsonValue) bool {
	isTarget := func(param *jsonValue) bool { return param.isString && param.text == "target" }
	return slices.ContainsFunc(function.values("variadic_params"), isTarget) ||
		slices.ContainsFunc(function.values("params"), func(params *jsonValue) bool { return slices.ContainsFunc(params.elements, isTarget) })
}

// rewriteJSON rewrites a JSON definition in the same way as rewriteHCL. A
// target with project_id gets labels given as a template that merges the
// project into its labels, because the labels can be any JSON value.
func rewriteJSON(data []byte, selects bool) ([]byte, int, bool) {
	root, err := parseJSON(data)
	if err != nil {
		return nil, 0, false
	}

	templates := map[*jsonValue]string{}
	uses := 0
	root.visitTemplates(func(s *jsonValue) {
		out, reads := rewriteTemplate(s.text, selects, nil)
		uses += reads
		templates[s] = out
	})
	var moves []replacement
	for _, target := range root.blocks("target") {
		members := target.body.members
		i := slices.IndexFunc(members, func(m jsonMember) bool { return m.key.text == projectIDAttribute })
		if i == -1 {
			continue
		}
		uses++
		project := jsonExpression(data, members[i].value, templates)
		if len(target.body.values("matrix")) == 0 {
			self := &targetProject{target: target.label, expression: project}
			target.body.visitStrings(func(s *jsonValue) {
				if _, ok := templates[s]; ok && !members[i].value.contains(s) {
					templates[s], _ = rewriteTemplate(s.text, selects, self)
				}
			})
		}
		labels := `{ "` + unsetLabelsLabel + `" = "true" }`
		existing := target.body.values("labels")
		if len(existing) > 0 && !existing[len(existing)-1].isNull(data) {
			labels = jsonExpression(data, existing[len(existing)-1], templates)
		}
		value := jsonString("${merge(" + labels + ", " + projectLabels(project) + ")}")
		if len(existing) == 0 {
			moves = append(moves,
				replacement{start: members[i].key.start, end: members[i].key.end, text: jsonString("labels")},
				replacement{start: members[i].value.start, end: members[i].value.end, text: value})
			continue
		}
		removed := make([]bool, len(members))
		removed[i] = true
		last := existing[len(existing)-1]
		moves = append(moves, replacement{start: last.start, end: last.end, text: value})
		moves = append(moves, memberRemovals(data, members, removed)...)
	}

	var edits []replacement
	for s, text := range templates {
		if text != s.text {
			edits = append(edits, replacement{start: s.start, end: s.end, text: jsonString(text)})
		}
	}
	return replace(data, slices.Concat(moves, outside(edits, moves))), uses, true
}

// jsonExpression returns an HCL expression with the value of a JSON value in
// a definition, with the templates rewritten.
func jsonExpression(data []byte, v *jsonValue, templates map[*jsonValue]string) string {
	template := func(s *jsonValue) string {
		if text, ok := templates[s]; ok {
			return templateExpression(text)
		}
		return templateExpression(s.text)
	}
	switch {
	case v.isString:
		return template(v)
	case v.isObject:
		items := make([]string, len(v.members))
		for i, m := range v.members {
			items[i] = "(" + template(m.key) + ") = " + jsonExpression(data, m.value, templates)
		}
		return "{ " + strings.Join(items, ", ") + " }"
	case data[v.start] == '[':
		elements := make([]string, len(v.elements))
		for i, element := range v.elements {
			elements[i] = jsonExpression(data, element, templates)
		}
		return "[" + strings.Join(elements, ", ") + "]"
	}
	return string(data[v.start:v.end])
}

// templateExpression returns an HCL expression with the value of a
// template.
func templateExpression(s string) string {
	expr, diags := hclsyntax.ParseTemplate([]byte(s), "", hcl.InitialPos)
	if wrap, ok := expr.(*hclsyntax.TemplateWrapExpr); ok && !diags.HasErrors() {
		inner := s[wrap.Wrapped.Range().Start.Byte:wrap.Wrapped.Range().End.Byte]
		return "(" + inner + heredocEnd(inner) + ")"
	}
	if !strings.Contains(s, "${") && !strings.Contains(s, "%{") {
		return string(jsonString(s))
	}
	delimiter := "DEPOT_TEMPLATE"
	for slices.ContainsFunc(strings.Split(s, "\n"), func(line string) bool { return strings.TrimSpace(line) == delimiter }) {
		delimiter += "_"
	}
	return "trimsuffix(<<" + delimiter + "\n" + s + "\n" + delimiter + "\n" + `, "\n")`
}

// memberRemovals returns the replacements that blank the removed members of
// an object, with the commas that separate them from the other members.
func memberRemovals(data []byte, members []jsonMember, removed []bool) []replacement {
	var out []replacement
	kept := slices.Index(removed, false)
	if kept == -1 {
		if len(members) > 0 {
			out = append(out, blankBytes(data, members[0].key.start, members[len(members)-1].value.end))
		}
		return out
	}
	if kept > 0 {
		out = append(out, blankBytes(data, members[0].key.start, members[kept].key.start))
	}
	for i := kept + 1; i < len(members); i++ {
		if removed[i] {
			out = append(out, blankBytes(data, members[i-1].value.end, members[i].value.end))
		}
	}
	return out
}

func jsonString(s string) []byte {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(s)
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}
