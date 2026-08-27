#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

CODEQL_VERSION="v2.26.4"
CODEQL_ZIP="$ROOT_DIR/.tools/codeql-linux64.zip"
CODEQL_DIR="$ROOT_DIR/.tools/codeql"
RESULTS_DIR="$ROOT_DIR/.codeql-results"
DB_DIR="$ROOT_DIR/.codeql-db"

log() {
  printf '[codeql-local] %s\n' "$*"
}

install_codeql_if_needed() {
  if [[ -x "$CODEQL_DIR/codeql" ]]; then
    return
  fi
  if command -v codeql >/dev/null 2>&1; then
    return
  fi

  log "Installing CodeQL CLI ${CODEQL_VERSION} to .tools/"
  mkdir -p "$ROOT_DIR/.tools"
  curl -L --fail -o "$CODEQL_ZIP" "https://github.com/github/codeql-cli-binaries/releases/download/${CODEQL_VERSION}/codeql-linux64.zip"
  unzip -q -o "$CODEQL_ZIP" -d "$ROOT_DIR/.tools"
}

resolve_codeql_bin() {
  if [[ -x "$CODEQL_DIR/codeql" ]]; then
    printf '%s\n' "$CODEQL_DIR/codeql"
    return
  fi
  command -v codeql
}

pack_for_language() {
  case "$1" in
    go) printf '%s\n' 'codeql/go-queries:codeql-suites/go-code-scanning.qls' ;;
    javascript-typescript) printf '%s\n' 'codeql/javascript-queries:codeql-suites/javascript-code-scanning.qls' ;;
    actions) printf '%s\n' 'codeql/actions-queries:codeql-suites/actions-code-scanning.qls' ;;
    *)
      printf 'Unsupported language: %s\n' "$1" >&2
      exit 1
      ;;
  esac
}

build_mode_for_language() {
  case "$1" in
    go) printf '%s\n' 'autobuild' ;;
    javascript-typescript|actions) printf '%s\n' 'none' ;;
    *)
      printf 'Unsupported language: %s\n' "$1" >&2
      exit 1
      ;;
  esac
}

create_db() {
  local codeql_bin="$1"
  local language="$2"
  local mode
  mode="$(build_mode_for_language "$language")"

  rm -rf "$DB_DIR/$language"

  if [[ "$mode" == "autobuild" ]]; then
    "$codeql_bin" database create "$DB_DIR/$language" \
      --language="$language" \
      --source-root="$ROOT_DIR" \
      --build-mode=autobuild
  else
    "$codeql_bin" database create "$DB_DIR/$language" \
      --language="$language" \
      --source-root="$ROOT_DIR" \
      --build-mode=none
  fi
}

analyze_db() {
  local codeql_bin="$1"
  local language="$2"
  local suite
  suite="$(pack_for_language "$language")"

  "$codeql_bin" pack download "${suite%%:*}"

  "$codeql_bin" database analyze "$DB_DIR/$language" "$suite" \
    --format=sarifv2.1.0 \
    --output="$RESULTS_DIR/$language.sarif" \
    --threads=0
}

main() {
  install_codeql_if_needed
  local codeql_bin
  codeql_bin="$(resolve_codeql_bin)"

  log "Using CodeQL: $($codeql_bin version | head -n 1)"

  local languages=("go" "javascript-typescript" "actions")
  if [[ $# -gt 0 ]]; then
    languages=("$@")
  fi

  rm -rf "$RESULTS_DIR" "$DB_DIR"
  mkdir -p "$RESULTS_DIR" "$DB_DIR"

  for lang in "${languages[@]}"; do
    log "Creating DB for $lang"
    create_db "$codeql_bin" "$lang"
    log "Analyzing $lang"
    analyze_db "$codeql_bin" "$lang"
  done

  if command -v jq >/dev/null 2>&1; then
    log "Summary by rule ID"
    jq -r '.runs[].results[]?.ruleId' "$RESULTS_DIR"/*.sarif | sort | uniq -c | sort -nr || true
  fi

  log "Done. SARIF files in $RESULTS_DIR"
}

main "$@"
