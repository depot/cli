# Authentication Troubleshooting Guide

This document explains how Depot CLI authentication works and provides troubleshooting steps for authentication issues, particularly when some commands work but others fail.

## Overview: Why `depot org list` Works but `depot projects list` Fails

The key difference lies in how these commands resolve authentication tokens:

### `depot org list` Authentication Flow

The `depot org list` command uses a **simple, direct token resolution**:

```go
// pkg/helpers/organization.go
func RetrieveOrganizations() ([]*Organization, error) {
    client := api.NewOrganizationsClient()
    req := corev1.ListOrganizationsRequest{}
    resp, err := client.ListOrganizations(
        context.Background(),
        api.WithAuthentication(connect.NewRequest(&req), config.GetApiToken()),
    )
    // ...
}
```

This directly reads from `~/.config/depot/depot.yaml` via `config.GetApiToken()`.

### `depot projects list` Authentication Flow

The `depot projects list` command uses a **complex token resolution** through `helpers.ResolveProjectAuth()`:

```go
// pkg/helpers/token.go
func ResolveProjectAuth(ctx context.Context, tok string) (string, error) {
    // 1. Check explicit token argument
    if tok != "" {
        return tok, nil
    }

    // 2. Check DEPOT_TOKEN environment variable
    if token := os.Getenv("DEPOT_TOKEN"); token != "" {
        return token, nil
    }

    // 3. Check config file (~/.config/depot/depot.yaml)
    if token := config.GetApiToken(); token != "" {
        return token, nil
    }

    // 4. Try OIDC providers (GitHub Actions, CircleCI, Buildkite, etc.)
    if token := resolveOIDCToken(ctx); token != "" {
        return token, nil
    }

    // 5. Check JIT tokens (DEPOT_JIT_TOKEN, DEPOT_CACHE_TOKEN)
    if token := resolveJITToken(); token != "" {
        return token, nil
    }

    // 6. If terminal, prompt for device authorization
    if IsTerminal() {
        return authorizeDevice(ctx)
    }

    return "", nil
}
```

## Root Cause Analysis

The "Invalid token" error on `depot projects list` while `depot org list` works suggests:

### Most Likely Cause: OIDC Token Interference

The server may have environment variables that trigger OIDC token resolution, returning an invalid or expired token **before** the valid config file token is checked.

OIDC providers check for these environment variables:

| Provider | Environment Variables Checked |
|----------|------------------------------|
| GitHub Actions | `ACTIONS_ID_TOKEN_REQUEST_TOKEN`, `ACTIONS_ID_TOKEN_REQUEST_URL` |
| CircleCI | `CIRCLE_OIDC_TOKEN`, `CIRCLE_OIDC_TOKEN_V2` |
| Buildkite | `BUILDKITE_OIDC_TOKEN`, `BUILDKITE_AGENT_ACCESS_TOKEN` |
| Actions Public | Various GitHub Actions variables |

If any of these environment variables are set (even with stale/invalid values), the CLI will attempt to use that OIDC token instead of the valid config file token.

## Diagnostic Steps

### Step 1: Check for OIDC Environment Variables

Run these commands on the problematic server:

```bash
# Check for any OIDC-related environment variables
env | grep -E "(ACTIONS_|CIRCLE_|BUILDKITE_)"

# Check for Depot-specific environment variables
env | grep -E "^DEPOT_"

# Enable OIDC debugging
export DEPOT_DEBUG_OIDC=1
depot projects list
```

### Step 2: Check Config File Token

```bash
# View the stored token
cat ~/.config/depot/depot.yaml

# Verify the token works directly
DEPOT_TOKEN=$(grep api_token ~/.config/depot/depot.yaml | awk '{print $2}') depot projects list
```

### Step 3: Bypass OIDC Resolution

Force using the explicit token:

```bash
# Extract and explicitly set the token
export DEPOT_TOKEN=$(grep api_token ~/.config/depot/depot.yaml | awk '{print $2}')
depot projects list
```

### Step 4: Enable Debug Logging

```bash
export DEPOT_DEBUG=1
export DEPOT_DEBUG_OIDC=1
depot projects list 2>&1 | tee depot-debug.log
```

## Backend Considerations

### Token Storage

From the CLI perspective, **tokens are NOT machine-specific in the backend**. The CLI stores tokens locally in:

- `~/.config/depot/depot.yaml` - Contains `api_token` and `org_id`
- `~/.config/depot/state.yaml` - Contains update check state (not auth-related)

The token returned from `depot login` is a user-level API token that should work from any machine.

### Token Validation

All API calls use the same authentication mechanism:

```go
// pkg/api/rpc.go
func WithAuthentication[T any](req *connect.Request[T], token string) *connect.Request[T] {
    req.Header().Add("Authorization", "Bearer "+token)
    return req
}
```

The backend validates the Bearer token. If you're seeing "unauthenticated: Invalid token", the issue is that:
1. An invalid token is being sent, OR
2. The correct token is being modified before sending, OR
3. There's a token format issue

## Solution: Code Improvement for Better Diagnostics

To help diagnose such issues in the future, consider adding verbose logging to the token resolution process:

```go
// Add to pkg/helpers/token.go
func ResolveProjectAuth(ctx context.Context, tok string) (string, error) {
    debug := os.Getenv("DEPOT_DEBUG_AUTH") != ""
    
    if tok != "" {
        if debug {
            fmt.Fprintf(os.Stderr, "[DEBUG] Using explicit token argument\n")
        }
        return tok, nil
    }

    if token := os.Getenv("DEPOT_TOKEN"); token != "" {
        if debug {
            fmt.Fprintf(os.Stderr, "[DEBUG] Using DEPOT_TOKEN environment variable\n")
        }
        return token, nil
    }

    if token := config.GetApiToken(); token != "" {
        if debug {
            fmt.Fprintf(os.Stderr, "[DEBUG] Using token from config file\n")
        }
        return token, nil
    }

    if token := resolveOIDCToken(ctx); token != "" {
        if debug {
            fmt.Fprintf(os.Stderr, "[DEBUG] Using OIDC token\n")
        }
        return token, nil
    }

    // ... rest of function
}
```

## Quick Fix Checklist

1. **Clear OIDC environment variables** on the problematic server:
   ```bash
   unset ACTIONS_ID_TOKEN_REQUEST_TOKEN
   unset ACTIONS_ID_TOKEN_REQUEST_URL
   unset CIRCLE_OIDC_TOKEN
   unset CIRCLE_OIDC_TOKEN_V2
   unset BUILDKITE_OIDC_TOKEN
   unset DEPOT_JIT_TOKEN
   unset DEPOT_CACHE_TOKEN
   ```

2. **Force explicit token usage**:
   ```bash
   export DEPOT_TOKEN="your-api-token"
   ```

3. **Full reset**:
   ```bash
   rm -rf ~/.config/depot
   depot login
   ```

4. **Verify config permissions**:
   ```bash
   ls -la ~/.config/depot/
   # Should be 0600 for depot.yaml
   ```

## Related Files

- `pkg/helpers/token.go` - Token resolution logic
- `pkg/helpers/organization.go` - Org list uses direct token
- `pkg/config/config.go` - Local config file handling
- `pkg/oidc/*.go` - OIDC provider implementations
- `pkg/api/rpc.go` - API client and authentication helpers
