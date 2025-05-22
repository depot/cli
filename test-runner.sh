#!/bin/bash
# Simple test script to run on any platform

echo "=== Depot Runner Detection Test ==="
echo "Platform: $(uname -s) $(uname -m)"
echo "Hostname: $(hostname)"
echo ""

# Set debug mode
export DEPOT_DEBUG_DETECTION=1

# Determine the binary name based on OS
if [[ "$OSTYPE" == "msys" || "$OSTYPE" == "win32" ]]; then
    BINARY="./depot-test.exe"
else
    BINARY="./depot-test"
fi

# Run a simple depot command that will trigger detection
echo "Running: $BINARY version"
$BINARY version 2>&1 | grep -E "(DEPOT DEBUG|depot v)"

echo ""
echo "=== Manual agentd check ==="

# Check for agentd based on OS
if [[ "$OSTYPE" == "msys" || "$OSTYPE" == "win32" ]]; then
    # Windows
    for path in "C:/Program Files/Depot/agentd.exe" "C:/ProgramData/Depot/agentd.exe" "C:/usr/local/bin/agentd.exe"; do
        if [[ -f "$path" ]]; then
            echo "✓ Found: $path"
        else
            echo "✗ Not found: $path"
        fi
    done
else
    # Linux/macOS
    if [[ -f "/usr/local/bin/agentd" ]]; then
        echo "✓ Found: /usr/local/bin/agentd"
        ls -la /usr/local/bin/agentd
    else
        echo "✗ Not found: /usr/local/bin/agentd"
    fi
fi