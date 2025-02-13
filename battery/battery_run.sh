#!/usr/bin/env bash
set -euo pipefail

###############################################################################
# CONFIG
###############################################################################
NAMESPACE="pvc-bench-operator-system"
CHECK_INTERVAL=10
TIMEOUT=3600
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

SINGLE_DIR="$SCRIPT_DIR/single"
SCALE_DIR="$SCRIPT_DIR/scale"
MASSIVE_DIR="$SCRIPT_DIR/massive"

RESULTS_DIR="$SCRIPT_DIR/results"

RESUME_AGE=0
SUMMARY_ONLY=false

###############################################################################
# HELPER: remove surrounding quotes if any.
###############################################################################
remove_quotes() {
  sed 's/^"//;s/"$//'
}

###############################################################################
# YAML PARSING HELPER FUNCTIONS
#
# These functions assume that the YAML files are formatted in a “regular”
# style (each key/value pair on its own line, using spaces for indentation).
###############################################################################

# Get a value from a block.
# Usage: get_yaml_field <file> <block> <key>
get_yaml_field() {
  local file="$1"
  local block="$2"
  local key="$3"
  sed -n "/^${block}:/,/^[^ ]/p" "$file" \
    | grep -E "^\s*${key}:" | head -n1 \
    | sed -E "s/^\s*${key}:\s*//" \
    | remove_quotes
}

# Get metadata.name
get_metadata_name() {
  local file="$1"
  get_yaml_field "$file" "metadata" "name"
}

# Get a value from within the "spec.scale" block.
# Usage: get_spec_scale_value <file> <key>
get_spec_scale_value() {
  local file="$1"
  local key="$2"
  sed -n '/^spec:/,/^[^ ]/p' "$file" \
    | sed -n '/^  scale:/,/^[^ ]/p' \
    | grep -E "^\s*${key}:" | head -n1 \
    | sed -E "s/^\s*${key}:\s*//" \
    | remove_quotes
}

# Get a value from within the "spec.test" block.
# Usage: get_spec_test_value <file> <key>
get_spec_test_value() {
  local file="$1"
  local key="$2"
  sed -n '/^spec:/,/^[^ ]/p' "$file" \
    | sed -n '/^  test:/,/^[^ ]/p' \
    | grep -E "^\s*${key}:" | head -n1 \
    | sed -E "s/^\s*${key}:\s*//" \
    | remove_quotes
}

# Get parameters from within the "spec.test.parameters" block.
# This will produce a comma-separated list of key=value pairs.
get_spec_test_parameters() {
  local file="$1"
  sed -n '/^spec:/,/^[^ ]/p' "$file" \
    | sed -n '/^  test:/,/^[^ ]/p' \
    | sed -n '/parameters:/,/^[^ ]/p' \
    | tail -n +2 \
    | grep ':' \
    | sed -E 's/^\s*([^:]+):\s*(.*)$/\1=\2/' \
    | paste -sd ', ' - \
    | sed 's/"//g'
}

# Get a value from within the "status" block for a given section.
# Usage: get_status_value <file> <section> <key>
get_status_value() {
  local file="$1"
  local section="$2"
  local key="$3"
  sed -n '/^status:/,/^[^ ]/p' "$file" \
    | sed -n "/^  ${section}:/,/^[^ ]/p" \
    | grep -E "^\s*${key}:" | head -n1 \
    | sed -E "s/^\s*${key}:\s*//" \
    | remove_quotes
}

###############################################################################
# CLEANUP on ERROR
###############################################################################
cleanupOnError() {
  echo "ERROR or interruption. Deleting all PVCBenchmark in namespace: $NAMESPACE"
  kubectl delete pvcbenchmark --all -n "$NAMESPACE" || true
}
trap cleanupOnError ERR INT

###############################################################################
# usage
###############################################################################
usage() {
  echo "Usage: $(basename "$0") [single|scale|massive|all] [options]"
  echo
  echo "Options:"
  echo "  --resume=<seconds>    Skip re-running tests if existing results are <seconds> old"
  echo "  --summary-only        Generate summaries only from existing .yaml, no test runs"
  echo
  echo "Examples:"
  echo "  $(basename "$0") single"
  echo "  $(basename "$0") all --resume=3600"
  echo "  $(basename "$0") all --summary-only"
  exit 1
}

###############################################################################
# parse_args
###############################################################################
parse_args() {
  if [[ $# -lt 1 ]]; then
    usage
  fi

  SUITE="$1"
  shift

  case "$SUITE" in
    single|scale|massive|all) ;;
    *) usage ;;
  esac

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --resume=*)
        RESUME_AGE="${1#--resume=}"
        if ! [[ "$RESUME_AGE" =~ ^[0-9]+$ ]]; then
          echo "ERROR: invalid --resume argument. Must be integer seconds."
          usage
        fi
        echo "Resume mode: skipping tests if results < $RESUME_AGE sec old."
        ;;
      --summary-only)
        SUMMARY_ONLY=true
        echo "SUMMARY-ONLY mode: skipping validation & test runs."
        ;;
      *)
        usage
        ;;
    esac
    shift
  done
}

###############################################################################
# validation
###############################################################################
validate_all_yaml() {
  local dir="$1"
  if [[ ! -d "$dir" ]]; then
    return
  fi

  local found_any=false
  for file in "$dir"/*.yaml; do
    if [[ ! -e "$file" ]]; then
      continue
    fi
    found_any=true
    echo "Validating (server dry-run): $file"
    kubectl apply --dry-run=server -f "$file" -n "$NAMESPACE"
  done
  if [[ "$found_any" = false ]]; then
    echo "No .yaml in $dir"
  fi
}

validate_suites() {
  for suite in "$@"; do
    local tdir
    case "$suite" in
      single)  tdir="$SINGLE_DIR"  ;;
      scale)   tdir="$SCALE_DIR"   ;;
      massive) tdir="$MASSIVE_DIR" ;;
      *) echo "Unknown suite: $suite"; exit 1;;
    esac
    if [[ -d "$tdir" ]]; then
      echo "Validating all YAML in $suite..."
      validate_all_yaml "$tdir"
    else
      echo "No directory for $suite"
    fi
  done
}

###############################################################################
# run_tests_in_dir <test_dir> <results_subdir>
###############################################################################
run_tests_in_dir() {
  local test_dir="$1"
  local results_subdir="$2"

  if [[ ! -d "$test_dir" ]]; then
    return
  fi

  local found=false
  for file in "$test_dir"/*.yaml; do
    if [[ ! -e "$file" ]]; then
      continue
    fi
    found=true

    local test_name
    test_name=$(get_metadata_name "$file")
    if [[ -z "$test_name" ]]; then
      echo "ERROR: missing metadata.name in $file"
      exit 1
    fi

    # check resume
    local out_file="$results_subdir/${test_name}.yaml"
    if [[ "$RESUME_AGE" -gt 0 && -f "$out_file" ]]; then
      local mod_time
      mod_time=$(stat -c %Y "$out_file" 2>/dev/null || echo 0)
      local now
      now=$(date +%s)
      local diff=$((now - mod_time))
      if [[ $diff -lt $RESUME_AGE ]]; then
        echo "Skipping '$test_name' => results only $diff s old (< $RESUME_AGE)."
        continue
      fi
    fi

    echo "========================================"
    echo "Applying test: $file"
    local start_run
    start_run=$(date +%s)

    kubectl apply -f "$file" -n "$NAMESPACE"

    while true; do
      local phase
      phase=$(kubectl get pvcbenchmark "$test_name" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || true)
      if [[ "$phase" == "Completed" ]]; then
        echo "PVCBenchmark '$test_name' => Completed."
        break
      fi
      local now
      now=$(date +%s)
      local elapsed=$((now - start_run))
      if [[ $elapsed -ge $TIMEOUT ]]; then
        echo "ERROR: Timeout waiting for $test_name"
        exit 1
      fi
      echo "  phase=$phase, sleeping $CHECK_INTERVAL s"
      sleep "$CHECK_INTERVAL"
    done

    local end_run
    end_run=$(date +%s)
    local total=$((end_run - start_run))
    echo "Test '$test_name' done in ${total}s"

    kubectl get pvcbenchmark "$test_name" -n "$NAMESPACE" -o yaml > "$out_file"
    echo "Saved results => $out_file"

    echo "Deleting PVCBenchmark '$test_name'"
    kubectl delete pvcbenchmark "$test_name" -n "$NAMESPACE"
  done

  if [[ "$found" = false ]]; then
    echo "No .yaml found in $test_dir"
  fi
}

###############################################################################
# Aggregator table functions
###############################################################################
print_aggregator_table() {
  local suite_col="$1"  # "" or suite name
  if [[ -z "$suite_col" ]]; then
    echo "## Aggregator Results"
    echo
    echo "| Test Name | readIOPS (avg/min–max [sum]) | writeIOPS (avg/min–max [sum]) | readBW (MB/s) (avg/min–max [sum]) | writeBW (MB/s) (avg/min–max [sum]) | readLat (ms) (avg/min–max [sum]) | writeLat (ms) (avg/min–max [sum]) | CPUUsage (avg/min–max [sum]) |"
    echo "|-----------|----------------------------|-----------------------------|-------------------------------------|--------------------------------------|------------------------------------|-------------------------------------|-----------------------------|"
  else
    echo "## Aggregator Results"
    echo
    echo "| Suite | Test Name | readIOPS (avg/min–max [sum]) | writeIOPS (avg/min–max [sum]) | readBW (MB/s) (avg/min–max [sum]) | writeBW (MB/s) (avg/min–max [sum]) | readLat (ms) (avg/min–max [sum]) | writeLat (ms) (avg/min–max [sum]) | CPUUsage (avg/min–max [sum]) |"
    echo "|-------|-----------|----------------------------|-----------------------------|-------------------------------------|--------------------------------------|------------------------------------|-------------------------------------|-----------------------------|"
  fi
}

aggregator_val() {
  local agg="$1"
  local file="$2"
  local avg minv maxv sumv
  avg=$(get_status_value "$file" "$agg" "avg")
  minv=$(get_status_value "$file" "$agg" "min")
  maxv=$(get_status_value "$file" "$agg" "max")
  sumv=$(get_status_value "$file" "$agg" "sum")
  if [[ "$avg" == "0.00" && "$minv" == "0.00" && "$maxv" == "0.00" && "$sumv" == "0.00" ]]; then
    echo ""
  else
    echo "$avg ($minv–$maxv) [$sumv]"
  fi
}

aggregator_row() {
  local suite_col="$1"
  local file="$2"
  local name
  name=$(get_metadata_name "$file")
  local readiops writeiops readbw writebw rlat wlat cpu
  readiops=$(aggregator_val "readIOPS" "$file")
  writeiops=$(aggregator_val "writeIOPS" "$file")
  readbw=$(aggregator_val "readBandwidth" "$file")
  writebw=$(aggregator_val "writeBandwidth" "$file")
  rlat=$(aggregator_val "readLatency" "$file")
  wlat=$(aggregator_val "writeLatency" "$file")
  cpu=$(aggregator_val "cpuUsage" "$file")
  
  if [[ -z "$suite_col" ]]; then
    echo "| $name | $readiops | $writeiops | $readbw | $writebw | $rlat | $wlat | $cpu |"
  else
    echo "| $suite_col | $name | $readiops | $writeiops | $readbw | $writebw | $rlat | $wlat | $cpu |"
  fi
}

###############################################################################
# Parameter table functions
###############################################################################
print_param_table() {
  local suite_col="$1"
  if [[ -z "$suite_col" ]]; then
    echo "## Parameters"
    echo
    echo "| Test Name | PVC Count | tool | parameters |"
    echo "|-----------|-----------|------|------------|"
  else
    echo "## Parameters"
    echo
    echo "| Suite | Test Name | PVC Count | tool | parameters |"
    echo "|-------|-----------|-----------|------|------------|"
  fi
}

param_row() {
  local suite_col="$1"
  local file="$2"
  local name pvc_count tool duration param_map merge
  name=$(get_metadata_name "$file")
  pvc_count=$(get_spec_scale_value "$file" "pvc_count")
  tool=$(get_spec_test_value "$file" "tool")
  duration=$(get_spec_test_value "$file" "duration")
  param_map=$(get_spec_test_parameters "$file")
  merge="$duration $param_map"
  if [[ -z "$suite_col" ]]; then
    echo "| $name | $pvc_count | $tool | $merge |"
  else
    echo "| $suite_col | $name | $pvc_count | $tool | $merge |"
  fi
}

###############################################################################
# parse_and_generate_md <dir> <suiteName>
###############################################################################
parse_and_generate_md() {
  local d="$1"
  local s="$2"
  local summary_file="$d/summary.md"

  echo "# Summary for $s suite" > "$summary_file"
  echo >> "$summary_file"

  {
    print_aggregator_table ""
    for f in "$d"/*.yaml; do
      [[ -e "$f" ]] || continue
      aggregator_row "" "$f"
    done
    echo
  } >> "$summary_file"

  {
    print_param_table ""
    for f in "$d"/*.yaml; do
      [[ -e "$f" ]] || continue
      param_row "" "$f"
    done
    echo
  } >> "$summary_file"

  echo "Suite summary saved to $summary_file"
}

###############################################################################
# combine_all_summaries
###############################################################################
combine_all_summaries() {
  local fm="$RESULTS_DIR/full_summary.md"
  echo "# Full Summary (all suites)" > "$fm"
  echo >> "$fm"

  {
    echo "## Aggregator Results"
    echo
    echo "| Suite | Test Name | readIOPS (avg/min–max [sum]) | writeIOPS (avg/min–max [sum]) | readBW (MB/s) (avg/min–max [sum]) | writeBW (MB/s) (avg/min–max [sum]) | readLat (ms) (avg/min–max [sum]) | writeLat (ms) (avg/min–max [sum]) | CPUUsage (avg/min–max [sum]) |"
    echo "|-------|-----------|----------------------------|-----------------------------|-------------------------------------|--------------------------------------|------------------------------------|-------------------------------------|-----------------------------|"

    if [[ -d "$RESULTS_DIR/single" ]]; then
      for f in "$RESULTS_DIR/single"/*.yaml; do
        [[ -e "$f" ]] || continue
        aggregator_row "single" "$f"
      done
    fi
    if [[ -d "$RESULTS_DIR/scale" ]]; then
      for f in "$RESULTS_DIR/scale"/*.yaml; do
        [[ -e "$f" ]] || continue
        aggregator_row "scale" "$f"
      done
    fi
    if [[ -d "$RESULTS_DIR/massive" ]]; then
      for f in "$RESULTS_DIR/massive"/*.yaml; do
        [[ -e "$f" ]] || continue
        aggregator_row "massive" "$f"
      done
    fi
    echo
  } >> "$fm"

  {
    echo "## Parameters"
    echo
    echo "| Suite | Test Name | PVC Count | tool | parameters |"
    echo "|-------|-----------|-----------|------|------------|"

    if [[ -d "$RESULTS_DIR/single" ]]; then
      for f in "$RESULTS_DIR/single"/*.yaml; do
        [[ -e "$f" ]] || continue
        param_row "single" "$f"
      done
    fi
    if [[ -d "$RESULTS_DIR/scale" ]]; then
      for f in "$RESULTS_DIR/scale"/*.yaml; do
        [[ -e "$f" ]] || continue
        param_row "scale" "$f"
      done
    fi
    if [[ -d "$RESULTS_DIR/massive" ]]; then
      for f in "$RESULTS_DIR/massive"/*.yaml; do
        [[ -e "$f" ]] || continue
        param_row "massive" "$f"
      done
    fi
    echo
  } >> "$fm"

  echo "Created combined summary at $fm"
}

###############################################################################
# run_suite <suiteName>
###############################################################################
run_suite() {
  local suite="$1"
  local test_dir=""
  local results_subdir=""

  case "$suite" in
    single)
      test_dir="$SINGLE_DIR"
      results_subdir="$RESULTS_DIR/single"
      ;;
    scale)
      test_dir="$SCALE_DIR"
      results_subdir="$RESULTS_DIR/scale"
      ;;
    massive)
      test_dir="$MASSIVE_DIR"
      results_subdir="$RESULTS_DIR/massive"
      ;;
    *)
      echo "Invalid suite '$suite'"
      usage
      ;;
  esac
  [[ -d "$test_dir" ]] || { echo "No directory for suite '$suite'"; return; }

  mkdir -p "$results_subdir"

  if [[ "$SUMMARY_ONLY" == false ]]; then
    echo "Running $suite from $test_dir"
    run_tests_in_dir "$test_dir" "$results_subdir"
  else
    echo "SUMMARY-ONLY => skip run for suite '$suite'"
  fi

  parse_and_generate_md "$results_subdir" "$suite"
}

###############################################################################
# MAIN
###############################################################################
parse_args "$@"

case "$SUITE" in
  single)
    if [[ "$SUMMARY_ONLY" == false ]]; then
      validate_suites single
    fi
    run_suite single
    ;;
  scale)
    if [[ "$SUMMARY_ONLY" == false ]]; then
      validate_suites scale
    fi
    run_suite scale
    ;;
  massive)
    if [[ "$SUMMARY_ONLY" == false ]]; then
      validate_suites massive
    fi
    run_suite massive
    ;;
  all)
    if [[ "$SUMMARY_ONLY" == false ]]; then
      validate_suites single scale massive
      run_suite single
      run_suite scale
      run_suite massive
    else
      echo "SUMMARY-ONLY for all => no validation or test runs."
      run_suite single
      run_suite scale
      run_suite massive
    fi
    combine_all_summaries
    ;;
  *)
    usage
    ;;
esac

echo "All requested suites done successfully."
