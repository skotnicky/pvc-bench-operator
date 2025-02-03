#!/usr/bin/env bash
set -euo pipefail

###############################################################################
# CONFIG
###############################################################################
NAMESPACE="pvc-bench-operator-system"
CHECK_INTERVAL=5
TIMEOUT=600  # 10 minutes
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

SINGLE_DIR="$SCRIPT_DIR/single"
SCALE_DIR="$SCRIPT_DIR/scale"
MASSIVE_DIR="$SCRIPT_DIR/massive"

RESULTS_DIR="$SCRIPT_DIR/results"

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
  echo "Usage: $(basename "$0") [single|scale|massive|all]"
  echo "  single  -> run tests in 'battery/single' only"
  echo "  scale   -> run tests in 'battery/scale' only"
  echo "  massive -> run tests in 'battery/massive' only"
  echo "  all     -> run single, scale, and massive suites sequentially"
  exit 1
}

###############################################################################
# parse_and_generate_md <suite_results_dir>
# Creates a 'summary.md' in that directory from each *.yaml
###############################################################################
parse_and_generate_md() {
  local suite_results_dir="$1"
  local summary_file="$suite_results_dir/summary.md"

  if ! command -v yq &>/dev/null; then
    echo "WARNING: yq not found. Cannot parse metrics. Skipping summary for $suite_results_dir."
    return
  fi

  echo "# Summary for $(basename "$suite_results_dir") suite" > "$summary_file"
  echo >> "$summary_file"

  # For each result .yaml file
  for result_yaml in "$suite_results_dir"/*.yaml; do
    [[ -e "$result_yaml" ]] || continue

    local test_name
    test_name=$(yq e '.metadata.name' "$result_yaml")
    local phase
    phase=$(yq e '.status.phase' "$result_yaml")

    echo "## Test: $test_name" >> "$summary_file"
    echo "**Phase**: $phase" >> "$summary_file"

    # We parse each aggregator if not all zero
    # Example fields: readIOPS, writeIOPS, readLatency, writeLatency, readBandwidth, writeBandwidth, cpuUsage

    # We'll define a small helper function inline via bash to reduce duplication
    parse_aggregator () {
      local aggregator_name="$1"
      # aggregator_name like 'readIOPS', 'writeLatency'
      local avg=$(yq e ".status.$aggregator_name.avg" "$result_yaml")
      local minv=$(yq e ".status.$aggregator_name.min" "$result_yaml")
      local maxv=$(yq e ".status.$aggregator_name.max" "$result_yaml")
      local sumv=$(yq e ".status.$aggregator_name.sum" "$result_yaml")
      # If all of them are '0.00', we skip
      if [[ "$avg" == "0.00" && "$minv" == "0.00" && "$maxv" == "0.00" && "$sumv" == "0.00" ]]; then
        # omit
        echo "" # no output
      else
        echo -e "**$aggregator_name**:\n- avg: $avg\n- min: $minv\n- max: $maxv\n- sum: $sumv\n"
      fi
    }

    # Now parse each aggregator
    local output=""
    for agg in readIOPS writeIOPS readLatency writeLatency readBandwidth writeBandwidth cpuUsage; do
      local snippet
      snippet="$(parse_aggregator "$agg")"
      if [[ -n "$snippet" ]]; then
        output+="$snippet"
      fi
    done

    if [[ -n "$output" ]]; then
      echo -e "$output" >> "$summary_file"
    else
      echo "No non-zero metrics." >> "$summary_file"
    fi

    echo >> "$summary_file"
  done

  echo "Summary saved to $summary_file"
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

  if [[ ! -d "$test_dir" ]]; then
    echo "No directory found for suite '$suite' at: $test_dir"
    return
  fi

  mkdir -p "$results_subdir"

  echo "----------------------------------------"
  echo "Running $suite tests from directory: $test_dir"
  echo "Results will go to: $results_subdir"

  local found_yaml=false
  for test_yaml in "$test_dir"/*.yaml; do
    if [[ ! -e "$test_yaml" ]]; then
      continue
    fi
    found_yaml=true

    echo "========================================"
    echo "Checking validity (dry-run) for: $test_yaml"
    kubectl apply --dry-run=server -f "$test_yaml" -n "$NAMESPACE"

    local test_start_time
    test_start_time=$(date +%s)

    echo "Dry-run OK. Applying: $test_yaml"
    kubectl apply -f "$test_yaml" -n "$NAMESPACE"

    # Extract name
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

    echo "Waiting for PVCBenchmark '$test_name' to become 'Completed'..."
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
        echo "ERROR: Timeout waiting for '$test_name' to complete (>$TIMEOUT seconds)."
        exit 1
      fi

      echo "  Phase='$phase'. Sleeping ${CHECK_INTERVAL}s..."
      sleep "$CHECK_INTERVAL"
    done

    # End time
    local test_end_time
    test_end_time=$(date +%s)
    local total_test_secs=$((test_end_time - test_start_time))
    echo "Test '$test_name' completed. Time spent: ${total_test_secs}s."

    # Save final CR
    local out_file="$results_subdir/${test_name}.yaml"
    kubectl get pvcbenchmark "$test_name" -n "$NAMESPACE" -o yaml > "$out_file"
    echo "Saved results to '$out_file'"

    # Cleanup
    echo "Deleting PVCBenchmark '$test_name' to clean up..."
    kubectl delete pvcbenchmark "$test_name" -n "$NAMESPACE"
  done

  if [[ "$found_yaml" = false ]]; then
    echo "No .yaml tests found in $test_dir"
  else
    # Generate a suite-level summary in 'summary.md'
    parse_and_generate_md "$results_subdir"
  fi
}

###############################################################################
# MAIN
###############################################################################
if [[ $# -lt 1 ]]; then
  usage
fi

mkdir -p "$RESULTS_DIR"

case "$1" in
  single)
    run_suite single
    ;;
  scale)
    run_suite scale
    ;;
  massive)
    run_suite massive
    ;;
  all)
    run_suite single
    run_suite scale
    run_suite massive
    ;;
  *)
    usage
    ;;
esac

echo "All requested suites completed successfully."
