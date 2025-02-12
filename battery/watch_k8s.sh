#!/bin/bash
# watch_cluster_overview.sh
#
# This script uses the watch command to continuously display an overview
# of your Kubernetes cluster, including:
#   - Pod counts grouped by status (with consistent number formatting)
#   - Node counts grouped by status (with consistent number formatting)
#   - PVC counts grouped by status (with consistent number formatting)
#   - Total number of ConfigMaps (formatted as "NNNNNN Created")
#   - Pods per Worker (number of pods scheduled on each worker node in the given namespace,
#     arranged in columns with 20 rows per column)
#
# Usage: ./watch_cluster_overview.sh [-i interval] [-n namespace]
#
# Options:
#   -i interval   Refresh interval in seconds (default: 2 seconds)
#   -n namespace  If provided, only resources in that namespace will be shown for
#                 Pods, PVCs, ConfigMaps, and Pods per Worker; otherwise, all namespaces are used.
#
# Dependencies: kubectl, watch

# Default values
INTERVAL=2
NAMESPACE=""

# Parse command-line options
while getopts ":i:n:" opt; do
  case $opt in
    i)
      INTERVAL=$OPTARG
      ;;
    n)
      NAMESPACE=$OPTARG
      ;;
    \?)
      echo "Invalid option: -$OPTARG" >&2
      exit 1
      ;;
    :)
      echo "Option -$OPTARG requires an argument." >&2
      exit 1
      ;;
  esac
done

# Check that required commands are available
if ! command -v kubectl &>/dev/null; then
    echo "Error: kubectl is not installed or not in your PATH."
    exit 1
fi

if ! command -v watch &>/dev/null; then
    echo "Error: watch is not installed or not in your PATH."
    exit 1
fi

# Set namespace options for namespaced resources
if [ -z "$NAMESPACE" ]; then
    NS_OPTION="--all-namespaces"
    NS_DISPLAY="All Namespaces"
else
    NS_OPTION="-n $NAMESPACE"
    NS_DISPLAY="$NAMESPACE"
fi

# Build the command string to be executed by watch.
# Command substitutions (like $(date)) are escaped so that they are evaluated on each refresh.
CMD="echo \"Kubernetes Cluster Overview (Namespace: ${NS_DISPLAY})\"; \
echo \"Updated: \$(date)\"; \
echo \"-----------------------------------------------------\"; \
echo \"Pods:\"; \
kubectl get pods ${NS_OPTION} --no-headers -o custom-columns=STATUS:.status.phase 2>/dev/null \
  | sort | uniq -c | sort -nr \
  | awk '{printf \"%6s %s\n\", \$1, \$2}'; \
echo \"\"; \
echo \"Nodes:\"; \
kubectl get nodes --no-headers 2>/dev/null \
  | awk '{print \$2}' | sort | uniq -c | sort -nr \
  | awk '{printf \"%6s %s\n\", \$1, \$2}'; \
echo \"\"; \
echo \"PersistentVolumeClaims:\"; \
kubectl get pvc ${NS_OPTION} --no-headers 2>/dev/null \
  | awk '{print \$2}' | sort | uniq -c | sort -nr \
  | awk '{printf \"%6s %s\n\", \$1, \$2}'; \
echo \"\"; \
echo \"ConfigMaps:\"; \
kubectl get configmaps ${NS_OPTION} --no-headers 2>/dev/null \
  | wc -l | awk '{printf \"%6d Created\n\", \$1}'; \
echo \"\"; \
echo \"Pods per Worker:\"; \
kubectl get pods ${NS_OPTION} --no-headers -o custom-columns=NODE:.spec.nodeName 2>/dev/null \
  | sort | uniq -c | sort -nr \
  | awk '{printf \"%6s %s\n\", \$1, \$2}' \
  | awk '{
       i = (NR-1) % 20;
       if (i in a) {
         a[i] = a[i] \"\t\" \$0;
       } else {
         a[i] = \$0;
       }
     }
     END {
       for(j = 0; j < 20; j++) {
         if(j in a) print a[j];
       }
     }'"

# Use the -t flag to hide the watch header.
# Execute the command string using bash -c so that embedded command substitutions are evaluated on each refresh.
exec watch -t -n "$INTERVAL" bash -c "$CMD"
