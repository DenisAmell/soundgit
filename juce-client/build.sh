#!/bin/bash
# Builds the helper and the plugin (AU, VST3, Standalone) and, on macOS,
# installs AU and VST3 for the current user. Needs CMake, a C++17 compiler and Go.
set -e
cd "$(dirname "$0")"
cmake -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build --parallel
ctest --test-dir build -C Release --output-on-failure
echo ""
echo "✓ Build OK"
