# Use a lightweight base, e.g., Debian or Ubuntu
FROM ubuntu:22.04
# Set the kubectl version you want to install.
# You can check https://github.com/kubernetes/kubernetes/tags for the latest releases.

# Install fio (and any other needed packages)
RUN apt-get update && apt-get install -y --no-install-recommends fio && \
    rm -rf /var/lib/apt/lists/*

ENTRYPOINT ["fio"]
