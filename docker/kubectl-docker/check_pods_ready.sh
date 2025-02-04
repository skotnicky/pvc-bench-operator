#!/bin/sh

NAMESPACE="$1"
LABEL_SELECTOR="$2"
POD_COUNT="$3"

echo "namespace=$NAMESPACE"
echo "label=$LABEL_SELECTOR"
echo "expected init container count=$POD_COUNT"

while true; do
    READY_COUNT=0
    for POD_NAME in $(kubectl get pods -n "$NAMESPACE" -l "$LABEL_SELECTOR" -o jsonpath='{.items[*].metadata.name}'); do
        # Get the "startedAt" timestamp of the init container "wait-for-other-pods"
        INIT_STARTED=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" \
          -o jsonpath='{.status.initContainerStatuses[?(@.name=="wait-for-other-pods")].state.running.startedAt}')
        
        if [ -n "$INIT_STARTED" ]; then
            echo "Pod $POD_NAME: init container is running (started at: $INIT_STARTED)"
            READY_COUNT=$((READY_COUNT + 1))
        else
            echo "Pod $POD_NAME: init container not running yet"
        fi
    done

    if [ "$READY_COUNT" -eq "$POD_COUNT" ]; then
        echo "All init containers are running"
        break
    fi

    echo "Waiting for all init containers to be running...($READY_COUNT/$POD_COUNT running)"
    sleep 5
done
