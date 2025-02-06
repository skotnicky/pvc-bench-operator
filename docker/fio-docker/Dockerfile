# Use Alpine as base image
FROM alpine:3.17

# Install fio
RUN apk add --no-cache fio

# Copy the entrypoint script into the image
COPY entrypoint.sh /usr/local/bin/entrypoint.sh

# Ensure the entrypoint script is executable
RUN chmod +x /usr/local/bin/entrypoint.sh

# Set the entrypoint to our script
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

# Default command to run fio if no arguments are provided
CMD ["fio"]
