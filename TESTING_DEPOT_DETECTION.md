# Testing Depot GHA Detection

This document outlines how to test the Depot GitHub Actions runner detection feature.

## What We're Testing

The CLI now detects when it's running on a Depot GitHub Actions runner and automatically requests private IP addresses for builder connections. This improves performance by using internal networking instead of public internet.

## Detection Method

The detection checks for the `agentd` binary in OS-specific locations:

- **Linux**: `/usr/local/bin/agentd`
- **macOS**: `/usr/local/bin/agentd`
- **Windows**: 
  - `C:\Program Files\Depot\agentd.exe`
  - `C:\ProgramData\Depot\agentd.exe`
  - `C:\usr\local\bin\agentd.exe`

## Test Programs

### 1. Standalone Detection Test

```bash
# Build the test program
go build -o test-detection ./cmd/test-detection

# Run with debug output
DEPOT_DEBUG_DETECTION=1 ./test-detection
```

### 2. Full CLI Test

```bash
# Build the CLI
go build -o depot-test ./cmd/depot

# Run any depot command with debug output
DEPOT_DEBUG_DETECTION=1 ./depot-test build .
```

## Expected Output

### On Depot Runners
```
[DEPOT DEBUG] Starting Depot GHA runner detection on linux/amd64
[DEPOT DEBUG] Checking for agentd at: /usr/local/bin/agentd
[DEPOT DEBUG] Found agentd at /usr/local/bin/agentd - Depot runner DETECTED
[DEPOT DEBUG] Requesting PRIVATE IP for builder connection
```

### On Regular GitHub/Local Machines
```
[DEPOT DEBUG] Starting Depot GHA runner detection on linux/amd64
[DEPOT DEBUG] Checking for agentd at: /usr/local/bin/agentd
[DEPOT DEBUG] agentd not found at /usr/local/bin/agentd: stat /usr/local/bin/agentd: no such file or directory
[DEPOT DEBUG] No agentd found - NOT a Depot runner
[DEPOT DEBUG] Using default (PUBLIC) IP for builder connection
```

## GitHub Actions Workflow

A test workflow is available at `.github/workflows/test-depot-detection.yml` that:
1. Tests on Depot runners (ubuntu, windows, macos)
2. Tests on regular GitHub runners for comparison
3. Shows debug output for verification

## Manual Testing Commands

### Windows (PowerShell)
```powershell
$env:DEPOT_DEBUG_DETECTION="1"
.\depot-test.exe version

# Check for agentd manually
Test-Path "C:\Program Files\Depot\agentd.exe"
Test-Path "C:\ProgramData\Depot\agentd.exe"
Test-Path "C:\usr\local\bin\agentd.exe"
```

### Linux/macOS
```bash
export DEPOT_DEBUG_DETECTION=1
./depot-test version

# Check for agentd manually
ls -la /usr/local/bin/agentd
```

## Verification Steps

1. **Build the CLI** with the changes
2. **Run on Depot runners** - Should detect and request private IPs
3. **Run on GitHub runners** - Should NOT detect, use public IPs
4. **Run locally** - Should NOT detect, use public IPs
5. **Check debug output** matches expected patterns

## Notes

- The debug output is only shown when `DEPOT_DEBUG_DETECTION=1` is set
- The detection happens during builder connection, not at CLI startup
- Private IP requests only affect builder connections, not API calls