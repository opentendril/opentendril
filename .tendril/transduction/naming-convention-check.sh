#!/bin/bash

# Transduction Signal: Naming Convention Enforcer
# This script runs during the pre-commit phase (Local CI).
# It scans all currently staged files for underscores in their filename.

echo "🌿 Transduction Check: Scanning staged files for naming convention violations..."

# Get list of staged files
STAGED_FILES=$(git diff --cached --name-only --diff-filter=ACM)

VIOLATIONS=0

for file in $STAGED_FILES; do
    # Extract just the filename from the path
    filename=$(basename "$file")
    
    # Check if filename contains an underscore
    if [[ "$filename" == *"_"* ]]; then
        echo "❌ [Transduction Blocked]: File '$file' contains an underscore. The project Genome requires kebab-case filenames." >&2
        VIOLATIONS=$((VIOLATIONS + 1))
    fi
done

if [ $VIOLATIONS -gt 0 ]; then
    echo "⚠️ Commit aborted due to $VIOLATIONS naming convention violation(s)." >&2
    echo "Please use 'git mv' to rename the files to kebab-case before attempting to commit again." >&2
    exit 1
fi

echo "✅ Transduction Check Passed: All staged files conform to the Genome."
exit 0
