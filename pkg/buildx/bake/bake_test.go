package bake_test

import (
	"context"
	"testing"

	"github.com/depot/cli/pkg/buildx/bake"
	"github.com/stretchr/testify/require"
)

func TestReadTargets(t *testing.T) {
	fp := bake.File{
		Name: "config.hcl",
		Data: []byte(`
target "webDEP" {
	args = {
		VAR_INHERITED = "webDEP"
		VAR_BOTH = "webDEP"
	}
	no-cache = true
	shm-size = "128m"
}

target "webapp" {
	dockerfile = "Dockerfile.webapp"
	args = {
		VAR_BOTH = "webapp"
	}
	platforms = [
		"linux/amd64"
	]
}
`),
	}

	targets, _, err := bake.ReadTargets(context.TODO(), []bake.File{fp}, []string{"webDEP"}, nil, nil)
	require.NoError(t, err)

	require.Contains(t, targets, "webDEP")
	require.Equal(t, "webDEP", *targets["webDEP"].Args["VAR_INHERITED"])
	require.Equal(t, "webDEP", *targets["webDEP"].Args["VAR_BOTH"])
	require.Equal(t, true, *targets["webDEP"].NoCache)
	require.Equal(t, "128m", *targets["webDEP"].ShmSize)

	targets2, _, err := bake.ReadTargets(context.TODO(), []bake.File{fp}, []string{"webapp"}, nil, nil)
	require.NoError(t, err)
	require.Contains(t, targets2, "webapp")
	require.Equal(t, "Dockerfile.webapp", *targets2["webapp"].Dockerfile)
	require.Equal(t, "webapp", *targets2["webapp"].Args["VAR_BOTH"])
	require.Equal(t, []string{"linux/amd64"}, targets2["webapp"].Platforms)
}

func TestVariableValidation(t *testing.T) {
	fp := bake.File{
		Name: "docker-bake.hcl",
		Data: []byte(`
variable "FOO" {
  validation {
    condition = FOO != ""
    error_message = "FOO is required."
  }
}
target "app" {
  args = {
    FOO = FOO
  }
}
`),
	}

	ctx := context.TODO()

	t.Run("Valid", func(t *testing.T) {
		t.Setenv("FOO", "bar")
		_, _, err := bake.ReadTargets(ctx, []bake.File{fp}, []string{"app"}, nil, nil)
		require.NoError(t, err)
	})

	t.Run("Invalid", func(t *testing.T) {
		_, _, err := bake.ReadTargets(ctx, []bake.File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "FOO is required.")
	})
}

func TestVariableValidationMulti(t *testing.T) {
	fp := bake.File{
		Name: "docker-bake.hcl",
		Data: []byte(`
variable "FOO" {
  validation {
    condition = FOO != ""
    error_message = "FOO is required."
  }
  validation {
    condition = strlen(FOO) > 4
    error_message = "FOO must be longer than 4 characters."
  }
}
target "app" {
  args = {
    FOO = FOO
  }
}
`),
	}

	ctx := context.TODO()

	t.Run("Valid", func(t *testing.T) {
		t.Setenv("FOO", "barbar")
		_, _, err := bake.ReadTargets(ctx, []bake.File{fp}, []string{"app"}, nil, nil)
		require.NoError(t, err)
	})

	t.Run("InvalidLength", func(t *testing.T) {
		t.Setenv("FOO", "bar")
		_, _, err := bake.ReadTargets(ctx, []bake.File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "FOO must be longer than 4 characters.")
	})

	t.Run("InvalidEmpty", func(t *testing.T) {
		_, _, err := bake.ReadTargets(ctx, []bake.File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "FOO is required.")
	})
}

func TestVariableValidationWithDeps(t *testing.T) {
	fp := bake.File{
		Name: "docker-bake.hcl",
		Data: []byte(`
variable "FOO" {}
variable "BAR" {
  validation {
    condition = FOO != ""
    error_message = "BAR requires FOO to be set."
  }
}
target "app" {
  args = {
    BAR = BAR
  }
}
`),
	}

	ctx := context.TODO()

	t.Run("Valid", func(t *testing.T) {
		t.Setenv("FOO", "foo")
		t.Setenv("BAR", "bar")
		_, _, err := bake.ReadTargets(ctx, []bake.File{fp}, []string{"app"}, nil, nil)
		require.NoError(t, err)
	})

	t.Run("Invalid", func(t *testing.T) {
		t.Setenv("BAR", "bar")
		_, _, err := bake.ReadTargets(ctx, []bake.File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "BAR requires FOO to be set.")
	})
}

func TestVariableValidationNumberType(t *testing.T) {
	fp := bake.File{
		Name: "docker-bake.hcl",
		Data: []byte(`
variable "FOO" {
  default = 0
  validation {
    condition = FOO > 5
    error_message = "FOO must be greater than 5."
  }
}
target "app" {
  args = {
    FOO = FOO
  }
}
`),
	}

	ctx := context.TODO()

	t.Run("Valid", func(t *testing.T) {
		t.Setenv("FOO", "10")
		_, _, err := bake.ReadTargets(ctx, []bake.File{fp}, []string{"app"}, nil, nil)
		require.NoError(t, err)
	})

	t.Run("Invalid", func(t *testing.T) {
		_, _, err := bake.ReadTargets(ctx, []bake.File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "FOO must be greater than 5.")
	})
}

func TestJSONOverridePriority(t *testing.T) {
	t.Run("JSON override ignored when same user var exists", func(t *testing.T) {
		dt := []byte(`
variable "FOO" {
    type = list(number)
}
variable "FOO_JSON" {
    type = list(number)
}

target "default" {
    args = {
        foo = FOO
    }
}`)
		// env FOO_JSON is the CSV override of var FOO_JSON, not a JSON override of FOO
		t.Setenv("FOO", "[1,2]")
		t.Setenv("FOO_JSON", "[3,4]")
		_, err := bake.ParseFile(dt, "docker-bake.hcl")
		require.ErrorContains(t, err, "failed to convert")
		require.ErrorContains(t, err, "from CSV")
	})

	t.Run("JSON override trumps CSV when no var name conflict", func(t *testing.T) {
		dt := []byte(`
variable "FOO" {
    type = list(number)
}

target "default" {
    args = {
        foo = length(FOO)
    }
}`)
		t.Setenv("FOO", "1,2")
		t.Setenv("FOO_JSON", "[3,4,5]")
		c, err := bake.ParseFile(dt, "docker-bake.hcl")
		require.NoError(t, err)
		require.Equal(t, 1, len(c.Targets))
		require.Equal(t, "3", *c.Targets[0].Args["foo"])
	})
}

func TestVariableInheritance(t *testing.T) {
	dt := []byte(`
variable "TAG" {
	default = "latest"
}

group "default" {
	targets = ["app", "db"]
}

target "app" {
	tags = ["myapp:${TAG}"]
}

target "db" {
	tags = ["mydb:${TAG}"]
}
`)

	t.Run("default", func(t *testing.T) {
		targets, groups, err := bake.ReadTargets(context.TODO(), []bake.File{{Name: "docker-bake.hcl", Data: dt}}, []string{"default"}, nil, nil)
		require.NoError(t, err)
		require.Contains(t, groups, "default")
		require.Len(t, targets, 2)
		require.Equal(t, []string{"myapp:latest"}, targets["app"].Tags)
		require.Equal(t, []string{"mydb:latest"}, targets["db"].Tags)
	})

	t.Run("override", func(t *testing.T) {
		t.Setenv("TAG", "v1.0")
		targets, _, err := bake.ReadTargets(context.TODO(), []bake.File{{Name: "docker-bake.hcl", Data: dt}}, []string{"app"}, nil, nil)
		require.NoError(t, err)
		require.Equal(t, []string{"myapp:v1.0"}, targets["app"].Tags)
	})
}

func TestTargetDescriptions(t *testing.T) {
	dt := []byte(`
group "default" {
	description = "Default group for all targets"
	targets = ["app"]
}

target "app" {
	description = "Main application target"
	tags = ["myapp:latest"]
}
`)

	targets, groups, err := bake.ReadTargets(context.TODO(), []bake.File{{Name: "docker-bake.hcl", Data: dt}}, []string{"default"}, nil, nil)
	require.NoError(t, err)

	require.Equal(t, "Default group for all targets", groups["default"].Description)
	require.Equal(t, "Main application target", targets["app"].Description)
}

func TestOverrideValidation(t *testing.T) {
	dt := []byte(`
variable "TAG" {
	default = "latest"
	validation {
		condition = TAG != ""
		error_message = "TAG cannot be empty"
	}
}

target "app" {
	tags = ["myapp:${TAG}"]
}
`)

	t.Run("valid override", func(t *testing.T) {
		_, _, err := bake.ReadTargets(context.TODO(), []bake.File{{Name: "docker-bake.hcl", Data: dt}}, []string{"app"}, []string{"app.tags=myapp:v1.0"}, nil)
		require.NoError(t, err)
	})

	t.Run("environment variable", func(t *testing.T) {
		t.Setenv("TAG", "")
		_, _, err := bake.ReadTargets(context.TODO(), []bake.File{{Name: "docker-bake.hcl", Data: dt}}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "TAG cannot be empty")
	})
}
