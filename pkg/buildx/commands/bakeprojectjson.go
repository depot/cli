package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
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

// blocks returns the bodies of the blocks of a type in a JSON definition.
func (v *jsonValue) blocks(blockType string) []*jsonValue {
	var out []*jsonValue
	for _, value := range v.values(blockType) {
		for _, labels := range value.objects() {
			for _, m := range labels.members {
				out = append(out, m.value.objects()...)
			}
		}
	}
	return out
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
		if hasJSONTargetParameter(function) {
			scopes = append(scopes, function.values("result")...)
		}
	}
	for _, target := range v.blocks("target") {
		if slices.ContainsFunc(target.values("matrix"), func(matrix *jsonValue) bool { return len(matrix.values("target")) > 0 }) {
			for _, m := range target.members {
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

// rewriteJSON rewrites a JSON definition in the same way as rewriteHCL.
func rewriteJSON(data []byte, selects bool) ([]byte, int, bool) {
	root, err := parseJSON(data)
	if err != nil {
		return nil, 0, false
	}

	templates := map[*jsonValue]string{}
	uses := 0
	root.visitTemplates(func(s *jsonValue) {
		out, reads := rewriteTemplate(s.text, selects)
		uses += reads
		templates[s] = out
	})
	var replacements []replacement
	for _, target := range root.blocks("target") {
		for _, m := range target.members {
			if m.key.text != projectIDAttribute {
				continue
			}
			uses++
			project := "${" + string(data[m.value.start:m.value.end]) + "}"
			if m.value.isString {
				project = m.value.text
			}
			if text, ok := templates[m.value]; ok {
				project = text
				delete(templates, m.value)
			}
			entry := append(append([]byte(`[{"filename": `), jsonString(projectPolicyPrefix+project)...), "}]"...)
			replacements = append(replacements,
				replacement{start: m.key.start, end: m.key.end, text: jsonString("policy")},
				replacement{start: m.value.start, end: m.value.end, text: entry})
		}
	}
	for s, text := range templates {
		if text != s.text {
			replacements = append(replacements, replacement{start: s.start, end: s.end, text: jsonString(text)})
		}
	}
	return replace(data, replacements), uses, true
}

func jsonString(s string) []byte {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(s)
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}
