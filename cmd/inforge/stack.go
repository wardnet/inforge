package main

import (
	"context"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/wardnet/inforge/program"
)

func upsertStack(ctx context.Context, stackName string, projCfg projectConfig) (auto.Stack, error) {
	proj := workspace.Project{
		Name:    tokens.PackageName(projCfg.Name),
		Runtime: workspace.NewProjectRuntimeInfo("go", nil),
		Backend: &workspace.ProjectBackend{URL: projCfg.Backend.URL},
	}
	return auto.UpsertStackInlineSource(ctx, stackName, projCfg.Name, program.Run,
		auto.Project(proj),
		auto.WorkDir("."),
	)
}

func applyStackConfig(ctx context.Context, s auto.Stack, stackCfg stackConfig) error {
	if len(stackCfg.Config) == 0 {
		return nil
	}
	cfgMap := make(auto.ConfigMap, len(stackCfg.Config))
	for k, v := range stackCfg.Config {
		cfgMap[k] = auto.ConfigValue{Value: v}
	}
	return s.SetAllConfig(ctx, cfgMap)
}
