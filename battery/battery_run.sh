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
    echo "Validating (dry-run=server): $file"
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

    # parse test_name
    local test_name
    test_name="$(cat "$file" | yq '.metadata.name' |  sed 's/\"//g')"
    if [[ -z "$test_name" ]]; then
      echo "ERROR: missing .metadata.name in $file"
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
# aggregator table (8 or 9 columns if suite_col used)
###############################################################################
print_aggregator_table() {
  local suite_col="$1"  # "" or suite name "single"
  if [[ -z "$suite_col" ]]; then
    # single suite => 8 columns
    echo "## Aggregator Results"
    echo
    echo "| Test Name | readIOPS | writeIOPS | readBW (MB/s) | writeBW (MB/s) | readLat (ms) | writeLat (ms) | CPUUsage |"
    echo "|-----------|----------|-----------|---------------|----------------|--------------|---------------|----------|"
  else
    # "all" mode => 9 columns with first col=Suite
    echo "## Aggregator Results"
    echo
    echo "| Suite | Test Name | readIOPS | writeIOPS | readBW (MB/s) | writeBW (MB/s) | readLat (ms) | writeLat (ms) | CPUUsage |"
    echo "|-------|-----------|----------|-----------|---------------|----------------|--------------|---------------|----------|"
  fi
}

# aggregator row for a single test
aggregator_row() {
  local suite_col="$1"
  local file="$2"

  local name
  name="$(cat "$file" | yq '.metadata.name // ""' -)"

  local readiops avg minv maxv
  readiops="$(aggregator_val 'readIOPS' "$file")"
  local writeiops
  writeiops="$(aggregator_val 'writeIOPS' "$file")"
  local readbw
  readbw="$(aggregator_val 'readBandwidth' "$file")"
  local writebw
  writebw="$(aggregator_val 'writeBandwidth' "$file")"
  local rlat
  rlat="$(aggregator_val 'readLatency' "$file")"
  local wlat
  wlat="$(aggregator_val 'writeLatency' "$file")"
  local cpu
  cpu="$(aggregator_val 'cpuUsage' "$file")"

  if [[ -z "$suite_col" ]]; then
    # single suite => 8 columns
    echo "| $name | $readiops | $writeiops | $readbw | $writebw | $rlat | $wlat | $cpu |"
  else
    # all => 9 columns
    echo "| $suite_col | $name | $readiops | $writeiops | $readbw | $writebw | $rlat | $wlat | $cpu |"
  fi
}

aggregator_val() {
  local agg="$1"
  local file="$2"
  local avg minv maxv
  avg="$(cat "$file" | yq ".status.${agg}.avg // \"\"" -)"
  minv="$(cat "$file" | yq ".status.${agg}.min // \"\"" -)"
  maxv="$(cat "$file" | yq ".status.${agg}.max // \"\"" -)"
  if [[ "$avg" == "0.00" && "$minv" == "0.00" && "$maxv" == "0.00" ]]; then
    echo ""
  else
    echo "$avg ($minv–$maxv)"
  fi
}

###############################################################################
# param table
###############################################################################
print_param_table() {
  local suite_col="$1"
  if [[ -z "$suite_col" ]]; then
    # single => 4 columns
    echo "## Parameters"
    echo
    echo "| Test Name | tool | parameters |"
    echo "|-----------|------|------------|"
  else
    echo "## Parameters"
    echo
    echo "| Suite | Test Name | tool | parameters |"
    echo "|-------|-----------|------|------------|"
  fi
}

param_row() {
  local suite_col="$1"
  local file="$2"

  local name
  name="$(cat "$file" | yq '.metadata.name // ""' -)"
  local tool
  tool="$(cat "$file" | yq '.spec.test.tool // ""' -)"
  local duration
  duration="$(cat "$file" | yq '.spec.test.duration // ""' -)"
  
  # parse param_map
  local param_map
  param_map="$(cat "$file" | yq '
    (.spec.test.parameters // {})
    | to_entries
    | map(.key + "=" + (.value|tostring))
    | join(", ")
  ' -)"
  merge="$duration $param_map"
  if [[ -z "$suite_col" ]]; then
    # single => 3 columns
    echo "| $name | $tool | $merge |"
  else
    echo "| $suite_col | $name | $tool | $merge |"
  fi
}

###############################################################################
# parse_and_generate_md <dir> <suiteName>
# => aggregator table + param table
###############################################################################
parse_and_generate_md() {
  local d="$1"
  local s="$2"
  local summary_file="$d/summary.md"

  if ! command -v yq &>/dev/null; then
    echo "WARNING: yq not found => no aggregator parse"
    return
  fi

  echo "# Summary for $s suite" > "$summary_file"
  echo >> "$summary_file"

  # aggregator table
  # if single/scale/massive => aggregator has 8 columns, param has 4
  # if s=all => aggregator has 9 col, param has 5 col => but we do that in the combine step
  # We'll handle 'single' 'scale' 'massive' => no suite col
  # We'll do aggregator + param.

  # aggregator
  {
    print_aggregator_table ""  # no suite col
    # read each .yaml, aggregator_row
    for f in "$d"/*.yaml; do
      [[ -e "$f" ]] || continue
      aggregator_row "" "$f"
    done
    echo
  } >> "$summary_file"

  # param
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
# => aggregator table with suite col, param table with suite col
###############################################################################
combine_all_summaries() {
  local fm="$RESULTS_DIR/full_summary.md"
  echo "# Full Summary (all suites)" > "$fm"
  echo >> "$fm"

  # aggregator
  {
    echo "## Aggregator Results"
    echo
    echo "| Suite | Test Name | readIOPS | writeIOPS | readBW | writeBW | readLat | writeLat | CPUUsage |"
    echo "|-------|-----------|----------|-----------|--------|---------|---------|----------|----------|"

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

  # param
  {
    echo "## Parameters"
    echo
    echo "| Suite | Test Name | tool | parameters |"
    echo "|-------|-----------|------|------------|"

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
