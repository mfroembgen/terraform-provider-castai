package castai

import (
	"context"
	"net/http"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"github.com/stretchr/testify/require"

	"github.com/castai/terraform-provider-castai/castai/sdk"
	mock_sdk "github.com/castai/terraform-provider-castai/castai/sdk/mock"
)

func TestWorkloadScalingPolicyHpaSettings(t *testing.T) {
	require.NoError(t, Provider("test").InternalValidate())

	input := map[string]any{
		"management_option": "READ_ONLY",
		fieldTakeOwnership:  false,
		fieldNativeHpaSpec: []any{map[string]any{
			fieldMinReplicas: 0,
			fieldMaxReplicas: 10,
			fieldMetrics: []any{
				map[string]any{
					FieldLimitStrategyType: "RESOURCE",
					fieldResource: []any{map[string]any{
						"name": "cpu",
						fieldTarget: []any{map[string]any{
							FieldLimitStrategyType: "UTILIZATION",
							"value":                "80",
						}},
					}},
				},
				map[string]any{
					FieldLimitStrategyType: "RESOURCE",
					fieldResource: []any{map[string]any{
						"name": "memory",
						fieldTarget: []any{map[string]any{
							FieldLimitStrategyType: "UTILIZATION",
							"value":                "70",
						}},
					}},
				},
			},
			fieldBehavior: []any{map[string]any{
				fieldScaleUp: []any{map[string]any{
					fieldStabilizationWindowSeconds: 0,
					fieldPolicies:                   []any{},
				}},
				fieldScaleDown: []any{map[string]any{
					fieldStabilizationWindowSeconds: 300,
					fieldSelectPolicy:               "MAX_CHANGE_POLICY_SELECT",
					fieldPolicies: []any{map[string]any{
						FieldLimitStrategyType: "PODS_SCALING_POLICY",
						"value":                1,
						"period_seconds":       60,
					}},
				}},
			}},
		}},
	}

	data := schema.TestResourceDataRaw(t, resourceWorkloadScalingPolicy().Schema, map[string]any{
		FieldHpaSettings: []any{input},
	})
	settings, err := toHpaSettings(toSection(data, FieldHpaSettings))
	require.NoError(t, err)
	require.False(t, settings.TakeOwnership)
	require.Zero(t, settings.NativeHpaSpec.MinReplicas)
	require.Equal(t, int32(0), *settings.NativeHpaSpec.Behavior.ScaleUp.StabilizationWindowSeconds)
	require.Nil(t, settings.NativeHpaSpec.Behavior.ScaleUp.Policies)
	require.Len(t, settings.NativeHpaSpec.Metrics, 2)

	flattened, err := toHpaSettingsMap(settings)
	require.NoError(t, err)
	require.NoError(t, data.Set(FieldHpaSettings, flattened))
	roundTripped, err := toHpaSettings(toSection(data, FieldHpaSettings))
	require.NoError(t, err)
	require.Equal(t, settings, roundTripped)

	// Omitted stabilization windows must get the Kubernetes defaults, not 0.
	omittedWindows := schema.TestResourceDataRaw(t, resourceWorkloadScalingPolicy().Schema, map[string]any{
		FieldHpaSettings: []any{map[string]any{
			"management_option": "READ_ONLY",
			fieldNativeHpaSpec: []any{map[string]any{
				fieldMinReplicas: 1,
				fieldMaxReplicas: 2,
				fieldBehavior: []any{map[string]any{
					fieldScaleUp:   []any{map[string]any{fieldSelectPolicy: "MAX_CHANGE_POLICY_SELECT"}},
					fieldScaleDown: []any{map[string]any{fieldSelectPolicy: "MAX_CHANGE_POLICY_SELECT"}},
				}},
			}},
		}},
	})
	defaults, err := toHpaSettings(toSection(omittedWindows, FieldHpaSettings))
	require.NoError(t, err)
	require.Equal(t, int32(0), *defaults.NativeHpaSpec.Behavior.ScaleUp.StabilizationWindowSeconds)
	require.Equal(t, int32(300), *defaults.NativeHpaSpec.Behavior.ScaleDown.StabilizationWindowSeconds)

	// Rules read back without a window must show the window an omitted one sends.
	unsetWindows := toHpaBehaviorMap(&sdk.WorkloadoptimizationV1HorizontalPodAutoscalerBehavior{
		ScaleUp:   &sdk.WorkloadoptimizationV1HPAScalingRules{},
		ScaleDown: &sdk.WorkloadoptimizationV1HPAScalingRules{},
	})[0]
	window := func(direction string) any {
		return unsetWindows[direction].([]map[string]any)[0][fieldStabilizationWindowSeconds]
	}
	require.Equal(t, 0, window(fieldScaleUp))
	require.Equal(t, 300, window(fieldScaleDown))

	invalid := map[string]any{
		fieldNativeHpaSpec: []any{map[string]any{
			fieldMinReplicas: 5,
			fieldMaxReplicas: 4,
		}},
	}
	require.EqualError(t, validateHpaSettings([]any{invalid}), "hpa_settings: max_replicas must be greater than or equal to min_replicas")

	unsupportedType := sdk.WorkloadoptimizationV1MetricSourceType("PODS")
	_, err = toHpaSettingsMap(&sdk.WorkloadoptimizationV1ScalingPolicyHPASettings{
		NativeHpaSpec: sdk.WorkloadoptimizationV1ScalingPolicyNativeHPASpec{
			Metrics: []sdk.WorkloadoptimizationV1MetricSpec{{Type: &unsupportedType}},
		},
	})
	require.EqualError(t, err, "hpa_settings.metrics.0: only RESOURCE metrics are supported")

	clusterID := "4e4cd9eb-82eb-407e-a926-e5fef81cab50"
	policyID := "98173807-6568-4e2b-9fe1-bcece3301649"
	require.NoError(t, data.Set(FieldClusterID, clusterID))
	require.NoError(t, data.Set("name", "native-hpa"))
	require.NoError(t, data.Set(FieldApplyType, "IMMEDIATE"))
	require.NoError(t, data.Set("management_option", "MANAGED"))

	client := mock_sdk.NewMockClientInterface(gomock.NewController(t))
	policy := &sdk.WorkloadoptimizationV1WorkloadScalingPolicy{
		Id:          policyID,
		ClusterId:   clusterID,
		Name:        "native-hpa",
		ApplyType:   sdk.WorkloadoptimizationV1ApplyType("IMMEDIATE"),
		HpaSettings: settings,
		RecommendationPolicies: sdk.WorkloadoptimizationV1RecommendationPolicies{
			ManagementOption: sdk.WorkloadoptimizationV1ManagementOption("MANAGED"),
		},
	}
	client.EXPECT().
		WorkloadOptimizationAPICreateWorkloadScalingPolicy(gomock.Any(), clusterID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, request sdk.WorkloadOptimizationAPICreateWorkloadScalingPolicyJSONRequestBody, _ ...sdk.RequestEditorFn) (*http.Response, error) {
			require.Equal(t, settings, request.HpaSettings)
			return toResponse(require.New(t), policy, http.StatusOK)
		})
	client.EXPECT().
		WorkloadOptimizationAPIGetWorkloadScalingPolicy(gomock.Any(), clusterID, policyID).
		DoAndReturn(func(_ context.Context, _, _ string, _ ...sdk.RequestEditorFn) (*http.Response, error) {
			return toResponse(require.New(t), policy, http.StatusOK)
		})

	diagnostics := resourceWorkloadScalingPolicy().CreateContext(t.Context(), data, &ProviderConfig{
		api: &sdk.ClientWithResponses{ClientInterface: client},
	})
	require.False(t, diagnostics.HasError())
	require.Equal(t, policyID, data.Id())

	// The shared flattening path must expose the same HPA definition to data sources.
	ds := dataSourceWorkloadScalingPolicy()
	dsData := schema.TestResourceDataRaw(t, ds.Schema, map[string]any{
		FieldClusterID: clusterID,
		FieldPolicyID:  policyID,
	})
	client.EXPECT().
		WorkloadOptimizationAPIGetWorkloadScalingPolicy(gomock.Any(), clusterID, policyID).
		DoAndReturn(func(_ context.Context, _, _ string, _ ...sdk.RequestEditorFn) (*http.Response, error) {
			return toResponse(require.New(t), policy, http.StatusOK)
		})
	meta := &ProviderConfig{api: &sdk.ClientWithResponses{ClientInterface: client}}
	require.Empty(t, ds.ReadContext(t.Context(), dsData, meta))
	dsSettings, err := toHpaSettings(toSection(dsData, FieldHpaSettings))
	require.NoError(t, err)
	require.Equal(t, settings, dsSettings)

	// An HPA-only change must reach the update API and survive the subsequent read.
	resource := resourceWorkloadScalingPolicy()
	data, err = schema.InternalMap(resource.Schema).Data(data.State(), &terraform.InstanceDiff{
		Attributes: map[string]*terraform.ResourceAttrDiff{
			"hpa_settings.0.native_hpa_spec.0.max_replicas": {Old: "10", New: "12"},
		},
	})
	require.NoError(t, err)
	updatedSettings := *settings
	updatedSettings.NativeHpaSpec.MaxReplicas = 12
	require.True(t, data.HasChange(FieldHpaSettings))
	for field := range resource.Schema {
		if field != FieldHpaSettings {
			require.False(t, data.HasChange(field), "unexpected change to %s", field)
		}
	}
	client.EXPECT().
		WorkloadOptimizationAPIUpdateWorkloadScalingPolicy(gomock.Any(), clusterID, policyID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, request sdk.WorkloadOptimizationAPIUpdateWorkloadScalingPolicyJSONRequestBody, _ ...sdk.RequestEditorFn) (*http.Response, error) {
			require.Equal(t, &updatedSettings, request.HpaSettings)
			policy.HpaSettings = request.HpaSettings
			return toResponse(require.New(t), policy, http.StatusOK)
		})
	client.EXPECT().
		WorkloadOptimizationAPIGetWorkloadScalingPolicy(gomock.Any(), clusterID, policyID).
		DoAndReturn(func(_ context.Context, _, _ string, _ ...sdk.RequestEditorFn) (*http.Response, error) {
			return toResponse(require.New(t), policy, http.StatusOK)
		})
	require.Empty(t, resource.UpdateContext(t.Context(), data, meta))
	updatedRoundTrip, err := toHpaSettings(toSection(data, FieldHpaSettings))
	require.NoError(t, err)
	require.Equal(t, &updatedSettings, updatedRoundTrip)

	// Removing the HPA block must send nil and clear stale HPA state on read.
	data, err = schema.InternalMap(resource.Schema).Data(data.State(), &terraform.InstanceDiff{
		Attributes: map[string]*terraform.ResourceAttrDiff{
			"hpa_settings.#": {Old: "1", New: "0"},
		},
	})
	require.NoError(t, err)
	client.EXPECT().
		WorkloadOptimizationAPIUpdateWorkloadScalingPolicy(gomock.Any(), clusterID, policyID, gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, request sdk.WorkloadOptimizationAPIUpdateWorkloadScalingPolicyJSONRequestBody, _ ...sdk.RequestEditorFn) (*http.Response, error) {
			require.Nil(t, request.HpaSettings)
			policy.HpaSettings = nil
			return toResponse(require.New(t), policy, http.StatusOK)
		})
	client.EXPECT().
		WorkloadOptimizationAPIGetWorkloadScalingPolicy(gomock.Any(), clusterID, policyID).
		DoAndReturn(func(_ context.Context, _, _ string, _ ...sdk.RequestEditorFn) (*http.Response, error) {
			return toResponse(require.New(t), policy, http.StatusOK)
		})
	require.Empty(t, resource.UpdateContext(t.Context(), data, meta))
	require.Empty(t, data.Get(FieldHpaSettings))
}

func TestWorkloadScalingPolicyHpaWindowPlan(t *testing.T) {
	const windowKey = "hpa_settings.0.native_hpa_spec.0.behavior.0.scale_down.0.stabilization_window_seconds"
	config := func(scaleDown map[string]any) map[string]any {
		return map[string]any{FieldHpaSettings: []any{map[string]any{
			"management_option": "READ_ONLY",
			fieldNativeHpaSpec: []any{map[string]any{
				fieldMinReplicas: 1,
				fieldMaxReplicas: 2,
				fieldBehavior:    []any{map[string]any{fieldScaleDown: []any{scaleDown}}},
			}},
		}}}
	}
	omitted := map[string]any{fieldSelectPolicy: "MAX_CHANGE_POLICY_SELECT"}
	zero := map[string]any{fieldSelectPolicy: "MAX_CHANGE_POLICY_SELECT", fieldStabilizationWindowSeconds: 0}
	resource := resourceWorkloadScalingPolicy()

	// Switching between an omitted window and an explicit 0 must plan an update.
	for _, transition := range [][2]map[string]any{{omitted, zero}, {zero, omitted}} {
		prior := schema.TestResourceDataRaw(t, resource.Schema, config(transition[0]))
		prior.SetId("policy")
		planned := terraform.NewResourceConfigRaw(config(transition[1]))
		diff, err := schema.InternalMap(resource.Schema).Diff(t.Context(), prior.State(), planned, nil, nil, true)
		require.NoError(t, err)
		require.NotNil(t, diff)
		require.Contains(t, diff.Attributes, windowKey)
	}
}
