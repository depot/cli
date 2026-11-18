package main

// These cases exercise the internal proxy with the Depot BuildKit exporter.
func fastLoadScenarios() []scenario {
	scenarios := []scenario{
		loadsImage(withRegistry(build("build-load-save-tags", "fast load preserves the user tag while saving to Depot", "basic",
			"--platform", "linux/amd64", "--load", "--save", "-t", "validation-{{run}}:latest"), "{{registry}}/{{project}}:{{run}}-b1")),
		loadsImage(withRegistry(bake("bake-load-save-tags", "bake fast load preserves the user tag while saving to Depot", "bake", "app", "--load", "--save",
			"--set", "app.tags=validation-{{run}}:latest", "--set", "app.platform=linux/amd64"), "{{registry}}/{{project}}:{{run}}-b1-app")),
		loadsImage(withAPI(build("build-load-pull-info-failure", "failed pull credential lookup still loads a multi-platform build under the user tag", "basic",
			"--platform", "linux/amd64,linux/arm64", "--load", "-t", "validation-{{run}}:latest"), apiBehavior{LoadUsingRegistry: true, PullInfoFails: true})),
		loadsImage(withAPI(bake("bake-load-pull-info-failure", "failed pull credential lookup still loads a multi-platform bake target under the user tag", "bake", "app", "--load",
			"--set", "app.tags=validation-{{run}}:latest", "--set", "app.platform=linux/amd64,linux/arm64"), apiBehavior{LoadUsingRegistry: true, PullInfoFails: true})),
	}
	for i := range scenarios {
		scenarios[i].Expect.StderrExcludes = append(scenarios[i].Expect.StderrExcludes, "fast load failed; retrying")
	}
	return scenarios
}
