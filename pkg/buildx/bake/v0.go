package bake

import (
	"github.com/depot/cli/pkg/buildx/bake/hclparser"
	hcl "github.com/hashicorp/hcl/v2"
	"github.com/pkg/errors"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

var (
	_ hclparser.WithEvalContexts = (*TargetV0)(nil)
	_ hclparser.WithGetName      = (*TargetV0)(nil)
)

type ConfigV0 struct {
	Targets []*TargetV0 `json:"target" hcl:"target,block" cty:"target"`
}

// TargetV0 has the fields that were changed from a list of strings to objects in v1.
// This allows us to be backwards compatible with v0.
type TargetV0 struct {
	// Name is used to find the target in the the []Target slice of the Config.
	Name string `json:"-" hcl:"name,label" cty:"name"`

	Attest    []string `json:"attest,omitempty" hcl:"attest,optional" cty:"attest"`
	CacheFrom []string `json:"cache-from,omitempty"  hcl:"cache-from,optional" cty:"cache-from"`
	CacheTo   []string `json:"cache-to,omitempty"  hcl:"cache-to,optional" cty:"cache-to"`
	Secrets   []string `json:"secret,omitempty" hcl:"secret,optional" cty:"secret"`
	SSH       []string `json:"ssh,omitempty" hcl:"ssh,optional" cty:"ssh"`
	Outputs   []string `json:"output,omitempty" hcl:"output,optional" cty:"output"`
	ProjectID string   `json:"project_id,omitempty" hcl:"project_id,optional" cty:"project_id"`
}

func (t *TargetV0) GetEvalContexts(ectx *hcl.EvalContext, block *hcl.Block, loadDeps func(hcl.Expression) hcl.Diagnostics) ([]*hcl.EvalContext, error) {
	content, _, err := block.Body.PartialContent(&hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{{Name: "matrix"}},
	})
	if err != nil {
		return nil, err
	}

	attr, ok := content.Attributes["matrix"]
	if !ok {
		return []*hcl.EvalContext{ectx}, nil
	}
	if diags := loadDeps(attr.Expr); diags.HasErrors() {
		return nil, diags
	}
	value, err := attr.Expr.Value(ectx)
	if err != nil {
		return nil, err
	}

	if !value.Type().IsMapType() && !value.Type().IsObjectType() {
		return nil, errors.Errorf("matrix must be a map")
	}
	matrix := value.AsValueMap()

	ectxs := []*hcl.EvalContext{ectx}
	for k, expr := range matrix {
		if !expr.CanIterateElements() {
			return nil, errors.Errorf("matrix values must be a list")
		}

		ectxs2 := []*hcl.EvalContext{}
		for _, v := range expr.AsValueSlice() {
			for _, e := range ectxs {
				e2 := ectx.NewChild()
				e2.Variables = make(map[string]cty.Value)
				if e != ectx {
					for k, v := range e.Variables {
						e2.Variables[k] = v
					}
				}
				e2.Variables[k] = v
				ectxs2 = append(ectxs2, e2)
			}
		}
		ectxs = ectxs2
	}
	return ectxs, nil
}

func (t *TargetV0) GetName(ectx *hcl.EvalContext, block *hcl.Block, loadDeps func(hcl.Expression) hcl.Diagnostics) (string, error) {
	content, _, diags := block.Body.PartialContent(&hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{{Name: "name"}, {Name: "matrix"}},
	})
	if diags != nil {
		return "", diags
	}

	attr, ok := content.Attributes["name"]
	if !ok {
		return block.Labels[0], nil
	}
	if _, ok := content.Attributes["matrix"]; !ok {
		return "", errors.Errorf("name requires matrix")
	}
	if diags := loadDeps(attr.Expr); diags.HasErrors() {
		return "", diags
	}
	value, diags := attr.Expr.Value(ectx)
	if diags != nil {
		return "", diags
	}

	value, err := convert.Convert(value, cty.String)
	if err != nil {
		return "", err
	}
	return value.AsString(), nil
}
