#!/usr/bin/env bash
set -euo pipefail

###############################################################################
# CONFIG
###############################################################################
NAMESPACE="pvc-bench-operator-system"
CHECK_INTERVAL=10
TIMEOUT=3600  # 60 minutes
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

SINGLE_DIR="$SCRIPT_DIR/single"
SCALE_DIR="$SCRIPT_DIR/scale"
MASSIVE_DIR="$SCRIPT_DIR/massive"

RESULTS_DIR="$SCRIPT_DIR/results"

# Default: no resume skip
RESUME_AGE=0  # in seconds (0 => disabled)

# By default, we do run tests. If --summary-only => skip run
SUMMARY_ONLY=false

###############################################################################
# CLEANUP on ERROR
###############################################################################
cleanupOnError() {
  echo "ERROR or interruption. Cleaning up all PVCBenchmark resources in namespace: $NAMESPACE"
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
  echo "  --summary-only        Do NOT validate or run any tests; only generate summaries from existing .yaml in results"
  echo
  echo "Examples:"
  echo "  $(basename "$0") single"
  echo "       => Validate & run tests in 'battery/single' only, produce summary."
  echo
  echo "  $(basename "$0") all --resume=3600"
  echo "       => Validate single/scale/massive, skip re-running tests whose results are <1 hour old, run the rest, produce all summaries."
  echo
  echo "  $(basename "$0") all --summary-only"
  echo "       => Generate summaries ONLY from existing .yaml in results, no test execution or validation."
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
    single|scale|massive|all) ;; # valid
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
        echo "Resume mode: skipping tests with fresh results (< $RESUME_AGE sec old)."
        ;;
      --summary-only)
        SUMMARY_ONLY=true
        echo "Summary-only mode: skipping validation & test runs."
        ;;
      *)
        usage
        ;;
    esac
    shift
  done
}

###############################################################################
# validate_all_yaml <dir>
# Checks each .yaml with server dry-run. If any fails, we exit immediately.
###############################################################################
validate_all_yaml() {
  local dir="$1"
  if [[ ! -d "$dir" ]]; then
    return
  fi

  local found_any=false
  for test_yaml in "$dir"/*.yaml; do
    if [[ ! -e "$test_yaml" ]]; then
      continue
    fi
    found_any=true
    echo "Validating (dry-run=server): $test_yaml"
    kubectl apply --dry-run=server -f "$test_yaml" -n "$NAMESPACE"
  done
  if [[ "$found_any" = false ]]; then
    echo "No .yaml tests found in $dir"
  fi
}

###############################################################################
# validate_suites <list_of_suites...>
###############################################################################
validate_suites() {
  for suite in "$@"; do
    local test_dir
    case "$suite" in
      single)  test_dir="$SINGLE_DIR" ;;
      scale)   test_dir="$SCALE_DIR"  ;;
      massive) test_dir="$MASSIVE_DIR" ;;
      *) echo "Unknown suite: $suite" && exit 1 ;;
    esac
    if [[ -d "$test_dir" ]]; then
      echo "Validating all YAML in $suite suite..."
      validate_all_yaml "$test_dir"
    else
      echo "No directory found for $suite suite"
    fi
  done
}

###############################################################################
# run_tests_in_dir <dir> <results_subdir>
###############################################################################
run_tests_in_dir() {
  local test_dir="$1"
  local results_subdir="$2"
  if [[ ! -d "$test_dir" ]]; then
    return
  fi

  local found_yaml=false
  for test_yaml in "$test_dir"/*.yaml; do
    if [[ ! -e "$test_yaml" ]]; then
      continue
    fi
    found_yaml=true

    # figure out test_name
    local test_name
    if command -v yq &>/dev/null; then
      test_name="$(yq e '.metadata.name' "$test_yaml")"
    else
      test_name="$(grep -m1 '^  name:' "$test_yaml" | awk '{print $2}')"
    fi
    if [[ -z "$test_name" ]]; then
      echo "ERROR: Could not determine 'metadata.name' from $test_yaml"
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
        echo "Skipping '$test_name' because existing results are only $diff s old (< $RESUME_AGE)."
        continue
      fi
    fi

    echo "========================================"
    echo "Applying test file: $test_yaml"
    local test_start_time
    test_start_time=$(date +%s)

    kubectl apply -f "$test_yaml" -n "$NAMESPACE"

    echo "Waiting for PVCBenchmark '$test_name' to become 'Completed' in '$NAMESPACE'..."
    local start_time
    start_time=$(date +%s)

    while true; do
      local phase
      phase=$(kubectl get pvcbenchmark "$test_name" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || true)
      if [[ "$phase" == "Completed" ]]; then
        echo "PVCBenchmark '$test_name' is Completed."
        break
      fi

      local now
      now=$(date +%s)
      local elapsed=$((now - start_time))
      if [[ $elapsed -ge $TIMEOUT ]]; then
        echo "ERROR: Timeout waiting for '$test_name' to complete (>$TIMEOUT sec)."
        exit 1
      fi

      echo "  Phase='$phase'. Sleeping ${CHECK_INTERVAL}s..."
      sleep "$CHECK_INTERVAL"
    done

    local test_end_time
    test_end_time=$(date +%s)
    local total_test_secs=$((test_end_time - test_start_time))
    echo "Test '$test_name' completed. Time spent: ${total_test_secs}s."

    kubectl get pvcbenchmark "$test_name" -n "$NAMESPACE" -o yaml > "$out_file"
    echo "Saved results to '$out_file'"

    echo "Deleting PVCBenchmark '$test_name' to clean up resources..."
    kubectl delete pvcbenchmark "$test_name" -n "$NAMESPACE"
  done

  if [[ "$found_yaml" = false ]]; then
    echo "No .yaml tests found in $test_dir"
  fi
}

###############################################################################
# parse_dir_into_table_rows <dir> <suiteCol>
# prints table rows for each .yaml in <dir>
###############################################################################
parse_dir_into_table_rows() {
  local dir="$1"
  local suite_col="$2"

  if ! command -v yq &>/dev/null; then
    return
  fi

  for result_yaml in "$dir"/*.yaml; do
    [[ -e "$result_yaml" ]] || continue

    local test_name
    test_name=$(yq e '.metadata.name' "$result_yaml")

    # parse test info
    local tool
    tool="$(yq e '.spec.test.tool // ""' "$result_yaml")"
    local duration
    duration="$(yq e '.spec.test.duration // ""' "$result_yaml")"

    local param_map
    param_map="$(yq e '
      (.spec.test.parameters // {})
      | to_entries
      | map(.key + "=" + (.value|tostring))
      | join(", ")
    ' "$result_yaml" 2>/dev/null || echo "")"

    local test_params=""
    if [[ -n "$tool" ]]; then
      test_params+="tool=$tool"
    fi
    if [[ -n "$duration" ]]; then
      [[ -n "$test_params" ]] && test_params+=", "
      test_params+="duration=$duration"
    fi
    if [[ -n "$param_map" ]]; then
      [[ -n "$test_params" ]] && test_params+=", "
      test_params+="$param_map"
    fi

    agg_cell() {
      local agg="$1"
      local base=".status.${agg}"
      local avg
      local minv
      local maxv
      avg=$(yq e "${base}.avg" "$result_yaml")
      minv=$(yq e "${base}.min" "$result_yaml")
      maxv=$(yq e "${base}.max" "$result_yaml")

      if [[ "$avg" == "0.00" && "$minv" == "0.00" && "$maxv" == "0.00" ]]; then
        echo ""
      else
        echo "${avg} (${minv}–${maxv})"
      fi
    }

    local riops="$(agg_cell readIOPS)"
    local wiops="$(agg_cell writeIOPS)"
    local rbw="$(agg_cell readBandwidth)"
    local wbw="$(agg_cell writeBandwidth)"
    local rlat="$(agg_cell readLatency)"
    local wlat="$(agg_cell writeLatency)"
    local cpu="$(agg_cell cpuUsage)"

    if [[ -n "$suite_col" ]]; then
      echo "| $suite_col | $test_name | $test_params | $riops | $wiops | $rbw | $wbw | $rlat | $wlat | $cpu |"
    else
      echo "| $test_name | $test_params | $riops | $wiops | $rbw | $wbw | $rlat | $wlat | $cpu |"
    fi
  done
}

###############################################################################
# parse_and_generate_md <results_subdir> <suiteName>
###############################################################################
parse_and_generate_md() {
  local dir="$1"
  local suite_name="$2"
  local summary_file="$dir/summary.md"

  if ! command -v yq &>/dev/null; then
    echo "WARNING: 'yq' not found. Skipping summary for $suite_name"
    return
  fi

  echo "# Summary for $suite_name suite" > "$summary_file"
  echo >> "$summary_file"
  echo "| Test Name | Test Params | readIOPS | writeIOPS | readBW | writeBW | readLat | writeLat | CPUUsage |" >> "$summary_file"
  echo "|-----------|------------|----------|-----------|--------|---------|---------|----------|----------|" >> "$summary_file"

  parse_dir_into_table_rows "$dir" "" >> "$summary_file"
  echo "Suite summary saved to $summary_file"
}

###############################################################################
# combine_all_summaries
###############################################################################
combine_all_summaries() {
  local full_md="$RESULTS_DIR/full_summary.md"
  echo "# Full Summary (all suites)" > "$full_md"
  echo >> "$full_md"
  echo "| Suite | Test Name | Test Params | readIOPS | writeIOPS | readBW | writeBW | readLat | writeLat | CPUUsage |" >> "$full_md"
  echo "|-------|-----------|------------|----------|-----------|--------|---------|---------|----------|----------|" >> "$full_md"

  if [[ -d "$RESULTS_DIR/single" ]]; then
    parse_dir_into_table_rows "$RESULTS_DIR/single" "single" >> "$full_md"
  fi
  if [[ -d "$RESULTS_DIR/scale" ]]; then
    parse_dir_into_table_rows "$RESULTS_DIR/scale" "scale" >> "$full_md"
  fi
  if [[ -d "$RESULTS_DIR/massive" ]]; then
    parse_dir_into_table_rows "$RESULTS_DIR/massive" "massive" >> "$full_md"
  fi

  echo "Created combined summary at $full_md"
}

###############################################################################
# run_suite <suiteName>
###############################################################################
run_suite() {
  local suite="$1"
  local test_dir=""
  local results_subdir=""
  local suite_label="$suite"

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

  if [[ ! -d "$test_dir" ]]; then
    echo "No directory found for suite '$suite' at: $test_dir"
    return
  fi

  mkdir -p "$results_subdir"

  if [[ "$SUMMARY_ONLY" == false ]]; then
    echo "Running $suite tests from directory: $test_dir"
    run_tests_in_dir "$test_dir" "$results_subdir"
  else
    echo "SUMMARY-ONLY mode: skipping run for suite '$suite'"
  fi

  parse_and_generate_md "$results_subdir" "$suite_label"
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
      # 1) Validate single, scale, massive first
      validate_suites single scale massive
      # 2) If all pass => run them
      run_suite single
      run_suite scale
      run_suite massive
    else
      echo "SUMMARY-ONLY mode for ALL suites (no validation or run)."
      run_suite single
      run_suite scale
      run_suite massive
    fi
    combine_all_summaries
    ;;
esac

echo "All requested suites completed successfully."
