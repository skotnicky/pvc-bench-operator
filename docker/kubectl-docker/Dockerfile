# Start from a lightweight base image
FROM alpine:3.18

# Set the kubectl version you want to install.
# You can check https://github.com/kubernetes/kubernetes/tags for the latest releases.
ARG KUBECTL_VERSION=v1.32.0

# Install curl, download kubectl, and make it executable
RUN apk add --no-cache curl ca-certificates && \
    curl -L "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" -o /usr/local/bin/kubectl && \
    chmod +x /usr/local/bin/kubectl

COPY check_pods_ready.sh /usr/local/bin/check_pods_ready.sh
RUN chmod +x /usr/local/bin/check_pods_ready.sh

ENTRYPOINT ["/usr/local/bin/check_pods_ready.sh"]
