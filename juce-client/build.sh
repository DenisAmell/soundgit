#!/bin/bash
set -e

cmake -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build --parallel
echo ""
echo "✓ Build OK"
