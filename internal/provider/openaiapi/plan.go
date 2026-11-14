package openaiapi

import (
	"context"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// KeepComputedValuesWhenUnchanged restores the prior state values of the named
// top-level computed attributes when the plan has marked them unknown but would
// otherwise equal the prior state.
//
// The framework marks computed attributes unknown whenever Terraform's proposed
// new state differs from the prior state. An unconfigured Optional+Computed
// nested object is proposed as null and restored by its own plan modifier, but
// by then sibling computed attributes are already unknown, which leaves a
// perpetual in-place update. Real changes keep the unknown values, so values
// such as updated_at are still refreshed from the API after an update.
func KeepComputedValuesWhenUnchanged(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse, names ...string) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || !req.Plan.Raw.IsKnown() {
		return
	}
	candidate, err := tftypes.Transform(req.Plan.Raw, func(attributePath *tftypes.AttributePath, value tftypes.Value) (tftypes.Value, error) {
		steps := attributePath.Steps()
		if len(steps) != 1 || value.IsKnown() {
			return value, nil
		}
		name, ok := steps[0].(tftypes.AttributeName)
		if !ok || !slices.Contains(names, string(name)) {
			return value, nil
		}
		prior, _, err := tftypes.WalkAttributePath(req.State.Raw, attributePath)
		if err != nil {
			return value, nil
		}
		priorValue, ok := prior.(tftypes.Value)
		if !ok {
			return value, fmt.Errorf("unexpected prior state value for %s", attributePath)
		}
		return priorValue, nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to compare planned and prior state", err.Error())
		return
	}
	if candidate.Equal(req.State.Raw) {
		resp.Plan.Raw = candidate
	}
}
