package bake

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestVariableValidationComprehensive tests all validation features from upstream
func TestVariableValidationComprehensive(t *testing.T) {
	t.Run("Basic validation", func(t *testing.T) {
		fp := File{
			Name: "docker-bake.hcl",
			Data: []byte(`
variable "FOO" {
  validation {
    condition = FOO != ""
    error_message = "FOO is required."
  }
}
target "app" {
  args = { FOO = FOO }
}
`),
		}

		t.Setenv("FOO", "bar")
		_, _, err := ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.NoError(t, err)

		// Test validation failure
		t.Setenv("FOO", "")
		_, _, err = ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "FOO is required.")
	})

	t.Run("Multiple validations", func(t *testing.T) {
		fp := File{
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
  args = { FOO = FOO }
}
`),
		}

		// Valid case
		t.Setenv("FOO", "barbar")
		_, _, err := ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.NoError(t, err)

		// First validation fails
		t.Setenv("FOO", "")
		_, _, err = ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "FOO is required.")

		// Second validation fails
		t.Setenv("FOO", "bar")
		_, _, err = ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "FOO must be longer than 4 characters.")
	})

	t.Run("Type constraints with validation", func(t *testing.T) {
		fp := File{
			Name: "docker-bake.hcl",
			Data: []byte(`
variable "PORT" {
  type = number
  default = 3000
  validation {
    condition = PORT > 0 && PORT < 65536
    error_message = "PORT must be between 1 and 65535."
  }
}
target "app" {
  args = { PORT = PORT }
}
`),
		}

		// Valid port
		t.Setenv("PORT", "8080")
		_, _, err := ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.NoError(t, err)

		// Invalid port range
		t.Setenv("PORT", "70000")
		_, _, err = ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "PORT must be between 1 and 65535.")

		// Invalid type
		t.Setenv("PORT", "not-a-number")
		_, _, err = ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
	})

	t.Run("List type with CSV and JSON", func(t *testing.T) {
		fp := File{
			Name: "docker-bake.hcl",
			Data: []byte(`
variable "TAGS" {
  type = list(string)
  default = ["latest"]
  validation {
    condition = length(TAGS) <= 3
    error_message = "Maximum of 3 tags allowed."
  }
}
target "app" {
  tags = TAGS
}
`),
		}

		// Valid CSV
		t.Setenv("TAGS", "v1.0,v1.1,latest")
		targets, _, err := ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.NoError(t, err)
		require.Len(t, targets["app"].Tags, 3)
		require.Equal(t, []string{"v1.0", "v1.1", "latest"}, targets["app"].Tags)

		// Valid JSON override
		t.Setenv("TAGS_JSON", `["v2.0","v2.1"]`)
		targets, _, err = ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.NoError(t, err)
		require.Len(t, targets["app"].Tags, 2)
		require.Equal(t, []string{"v2.0", "v2.1"}, targets["app"].Tags)

		// Clear other env vars and test invalid - too many tags
		os.Unsetenv("TAGS_JSON")
		t.Setenv("TAGS", "v1,v2,v3,v4")
		_, _, err = ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "Maximum of 3 tags allowed.")
	})

	t.Run("Variable dependencies", func(t *testing.T) {
		fp := File{
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
  args = { BAR = BAR }
}
`),
		}

		// Valid - both variables set
		t.Setenv("FOO", "foo")
		t.Setenv("BAR", "bar")
		_, _, err := ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.NoError(t, err)

		// Invalid - FOO not set but BAR references it
		os.Unsetenv("FOO")
		t.Setenv("BAR", "bar")
		_, _, err = ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "BAR requires FOO to be set.")
	})

	t.Run("Variable descriptions", func(t *testing.T) {
		fp := File{
			Name: "docker-bake.hcl",
			Data: []byte(`
variable "APP_NAME" {
  description = "Name of the application"
  type = string
  default = "myapp"
}
group "default" {
  description = "Default group for all targets"
  targets = ["app"]
}
target "app" {
  description = "Main application target"
  tags = ["${APP_NAME}:latest"]
}
`),
		}

		targets, groups, err := ReadTargets(context.TODO(), []File{fp}, []string{"default"}, nil, nil)
		require.NoError(t, err)

		// Check descriptions are preserved
		require.Equal(t, "Default group for all targets", groups["default"].Description)
		require.Equal(t, "Main application target", targets["app"].Description)

		// Check variable interpolation works
		require.Equal(t, []string{"myapp:latest"}, targets["app"].Tags)
	})
}

// TestBakeValidationIntegration tests validation in the context of full bake operations
func TestBakeValidationIntegration(t *testing.T) {
	// This test simulates the real-world usage that originally failed
	fp := File{
		Name: "test-bake.hcl",
		Data: []byte(`
variable "app_name" {
  description = "Name of the application"
  type        = string
  default     = "myapp"
  
  validation {
    condition     = app_name != ""
    error_message = "App name cannot be empty."
  }
  
  validation {
    condition     = strlen(app_name) <= 50
    error_message = "App name must be 50 characters or less."
  }
}

variable "port" {
  description = "Port number for the application"
  type        = number
  default     = 8080
  
  validation {
    condition     = port > 0 && port < 65536
    error_message = "Port must be between 1 and 65535."
  }
}

variable "tags" {
  description = "List of tags to apply"
  type        = list(string)
  default     = ["latest"]
  
  validation {
    condition     = length(tags) <= 3
    error_message = "Maximum of 3 tags allowed."
  }
}

group "default" {
  description = "Default group for building all targets"
  targets = ["app"]
}

target "app" {
  description = "Main application target"
  context = "."
  dockerfile = "Dockerfile"
  tags = ["${app_name}:${tags[0]}"]
  args = {
    PORT = port
  }
}
`),
	}

	t.Run("Default values work", func(t *testing.T) {
		targets, groups, err := ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.NoError(t, err)
		require.Contains(t, targets, "app")
		require.Contains(t, groups, "default")
		require.Equal(t, []string{"myapp:latest"}, targets["app"].Tags)
		require.Equal(t, "8080", *targets["app"].Args["PORT"])
	})

	t.Run("CSV list variables work", func(t *testing.T) {
		t.Setenv("tags", "v1.0,v1.1,latest")
		targets, _, err := ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.NoError(t, err)
		require.Equal(t, []string{"myapp:v1.0"}, targets["app"].Tags)
	})

	t.Run("JSON list variables work", func(t *testing.T) {
		t.Setenv("tags_JSON", `["v2.0","v2.1"]`)
		targets, _, err := ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.NoError(t, err)
		require.Equal(t, []string{"myapp:v2.0"}, targets["app"].Tags)
	})

	t.Run("Validation failures are caught", func(t *testing.T) {
		// Test each validation error separately to avoid interactions

		// Empty app name should fail
		t.Setenv("app_name", "")
		_, _, err := ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "App name cannot be empty.")

		// Reset and test too many tags
		os.Unsetenv("app_name")
		t.Setenv("tags", "v1,v2,v3,v4")
		_, _, err = ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "Maximum of 3 tags allowed.")

		// Reset and test invalid port
		os.Unsetenv("tags")
		t.Setenv("port", "70000")
		_, _, err = ReadTargets(context.TODO(), []File{fp}, []string{"app"}, nil, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "Port must be between 1 and 65535.")
	})
}
