#!/bin/sh

NAMESPACE="$1"
LABEL_SELECTOR="$2"
POD_COUNT="$3"

echo "namespace=$NAMESPACE"
echo "label=$LABEL_SELECTOR"
echo "fio_pods_count=$POD_COUNT"

while true; do
    READY_COUNT=0
    for POD_NAME in $(kubectl get pods -n "$NAMESPACE" -l "$LABEL_SELECTOR" -o jsonpath='{.items[*].metadata.name}'); do
        POD_STATUS=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
        if [ "$POD_STATUS" = "True" ]; then
            READY_COUNT=$(($READY_COUNT + 1))
        fi
    done
    if [ "$READY_COUNT" -eq "$POD_COUNT" ]; then
        echo "All pods are ready"
        break
    fi
    echo "Waiting for all pods to be ready..."
    sleep 5
done
