#!/usr/bin/env bash
# scripts/architecture.sh — architecture metrics report and guardrail check.
#
# Usage:
#   scripts/architecture.sh           # print report
#   scripts/architecture.sh check     # compare against baseline, exit non-zero if exceeded
#   scripts/architecture.sh update    # overwrite baseline with current counts

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASELINE="$ROOT/scripts/architecture.baseline"
MODE="${1:-report}"

# ── metric collectors ────────────────────────────────────────────────────────

# Directories under internal/ that contain at least one non-test .go file.
pkg_count() {
    find "$ROOT/internal" -name '*.go' ! -name '*_test.go' \
        | sed 's|/[^/]*$||' | sort -u | wc -l | tr -d '[:space:]'
}

# Exported and unexported interface type definitions in non-test .go files.
# Matches both `type X interface {` and `type X interface{`.
iface_count() {
    grep -rh --include='*.go' --exclude='*_test.go' \
        -E '^[[:space:]]*type [A-Za-z][A-Za-z0-9_]* interface[[:space:]]*\{' \
        "$ROOT/internal" "$ROOT/cmd" 2>/dev/null \
        | wc -l | tr -d '[:space:]'
}

# Non-test production files in internal/ that exceed 500 lines.
large_file_count() {
    find "$ROOT/internal" -name '*.go' ! -name '*_test.go' -print0 \
        | xargs -0 wc -l 2>/dev/null \
        | awk '$1 > 500 && $2 != "total"' \
        | wc -l | tr -d '[:space:]'
}

# Method definitions matching Set[A-Z]* (any receiver type) in non-test files.
set_mutator_count() {
    grep -rh --include='*.go' --exclude='*_test.go' \
        -E 'func \([^)]+\) Set[A-Z]' \
        "$ROOT/internal" 2>/dev/null \
        | wc -l | tr -d '[:space:]'
}

# Call sites of database.Open( in non-test files.
db_open_count() {
    grep -rh --include='*.go' --exclude='*_test.go' \
        'database\.Open(' \
        "$ROOT/internal" "$ROOT/cmd" 2>/dev/null \
        | wc -l | tr -d '[:space:]'
}

# ── report helpers ───────────────────────────────────────────────────────────

large_files_list() {
    find "$ROOT/internal" -name '*.go' ! -name '*_test.go' -print0 \
        | xargs -0 wc -l 2>/dev/null \
        | awk -v root="$ROOT/" '$1 > 500 && $2 != "total" {
            path = $2
            sub(root, "", path)
            printf "  %5d  %s\n", $1, path
          }' \
        | sort -rn
}

read_baseline() {
    local key="$1"
    grep "^${key}=" "$BASELINE" 2>/dev/null | head -1 | cut -d= -f2
}

# ── collect current counts ───────────────────────────────────────────────────

PACKAGES=$(pkg_count)
INTERFACES=$(iface_count)
LARGE_FILES=$(large_file_count)
SET_MUTATORS=$(set_mutator_count)
DB_OPENS=$(db_open_count)

# ── modes ────────────────────────────────────────────────────────────────────

if [[ "$MODE" == "update" ]]; then
    cat > "$BASELINE" <<EOF
packages=$PACKAGES
interfaces=$INTERFACES
large_files=$LARGE_FILES
set_mutators=$SET_MUTATORS
database_opens=$DB_OPENS
EOF
    echo "Updated $BASELINE"
    cat "$BASELINE"
    exit 0
fi

if [[ "$MODE" == "report" ]]; then
    printf "Architecture Metrics\n"
    printf "====================\n"
    if [[ -f "$BASELINE" ]]; then
        printf "%-20s %6s  %s\n" "metric" "now" "baseline"
        printf "%-20s %6s  %s\n" "------" "---" "--------"
        for key in packages interfaces large_files set_mutators database_opens; do
            b=$(read_baseline "$key")
            case "$key" in
                packages)      val="$PACKAGES"     ;;
                interfaces)    val="$INTERFACES"   ;;
                large_files)   val="$LARGE_FILES"  ;;
                set_mutators)  val="$SET_MUTATORS" ;;
                database_opens) val="$DB_OPENS"    ;;
            esac
            flag=""
            if [[ -n "$b" ]] && (( val > b )); then flag="  ← OVER"; fi
            printf "%-20s %6s  %s%s\n" "$key" "$val" "${b:-—}" "$flag"
        done
    else
        printf "%-20s %6s\n" "packages"       "$PACKAGES"
        printf "%-20s %6s\n" "interfaces"     "$INTERFACES"
        printf "%-20s %6s\n" "large_files"    "$LARGE_FILES"
        printf "%-20s %6s\n" "set_mutators"   "$SET_MUTATORS"
        printf "%-20s %6s\n" "database_opens" "$DB_OPENS"
    fi
    printf "\nLarge production files (>500 lines):\n"
    large_files_list
    exit 0
fi

if [[ "$MODE" == "check" ]]; then
    if [[ ! -f "$BASELINE" ]]; then
        echo "ERROR: baseline file not found: $BASELINE" >&2
        echo "Run 'scripts/architecture.sh update' to create it." >&2
        exit 1
    fi

    FAIL=0
    check_metric() {
        local name="$1" current="$2"
        local baseline
        baseline=$(read_baseline "$name")
        if [[ -z "$baseline" ]]; then
            printf "  ERROR  %-20s no baseline entry\n" "$name" >&2
            FAIL=1
            return
        fi
        if (( current > baseline )); then
            printf "  FAIL   %-20s current=%-5s baseline=%s\n" "$name" "$current" "$baseline" >&2
            FAIL=1
        else
            printf "  ok     %-20s current=%-5s baseline=%s\n" "$name" "$current" "$baseline"
        fi
    }

    echo "Architecture check:"
    check_metric packages       "$PACKAGES"
    check_metric interfaces     "$INTERFACES"
    check_metric large_files    "$LARGE_FILES"
    check_metric set_mutators   "$SET_MUTATORS"
    check_metric database_opens "$DB_OPENS"

    if (( FAIL )); then
        echo "" >&2
        echo "One or more architecture metrics exceed their baselines." >&2
        echo "If the growth is intentional, run: scripts/architecture.sh update" >&2
        exit 1
    fi
    exit 0
fi

echo "Usage: $0 [report|check|update]" >&2
exit 1
