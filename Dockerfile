# Base stages for different architectures
FROM --platform=linux/amd64 alpine:3.19.1 AS base-amd64
FROM --platform=linux/arm64 alpine:3.19.1 AS base-arm64

# Architecture-specific flockadoo stage
FROM base-${TARGETARCH} AS client-base
RUN apk add --no-cache curl unzip

# Add build argument for release version
ARG RELEASE_VERSION=latest

# Download and install flockadoo binary
RUN ARCH="$(uname -m)"; \
    case "${ARCH}" in \
        x86_64) ARCH_TYPE=amd64 ;; \
        aarch64|arm64) ARCH_TYPE=arm64 ;; \
        *) echo "Unsupported architecture: ${ARCH}" && exit 1 ;; \
    esac && \
    BINARY_NAME="flockadoo-linux-${ARCH_TYPE}" && \
    if [ "$RELEASE_VERSION" = "latest" ]; then \
        DOWNLOAD_URL="https://github.com/flockadoo/flockadoo/releases/latest/download/${BINARY_NAME}.zip"; \
    else \
        DOWNLOAD_URL="https://github.com/flockadoo/flockadoo/releases/download/v${RELEASE_VERSION}/${BINARY_NAME}.zip"; \
    fi && \
    echo "Downloading from: $DOWNLOAD_URL" && \
    curl -Lo "/tmp/${BINARY_NAME}.zip" "$DOWNLOAD_URL" && \
    unzip -o "/tmp/${BINARY_NAME}.zip" -d /tmp && \
    mv "/tmp/${BINARY_NAME}" /usr/local/bin/flockadoo

# Combine binaries into an intermediate archive stage
FROM scratch AS archive
COPY --from=client-base /usr/local/bin/flockadoo /bin/flockadoo

# Final minimal runtime stage
FROM debian:12-slim

# Install minimal requirements (split into multiple steps for better reliability in emulation)
RUN apt-get update
RUN apt-get install -y --no-install-recommends ca-certificates 
RUN apt-get install -y --no-install-recommends curl bash procps sudo unzip
RUN apt-get clean && rm -rf /var/lib/apt/lists/*

# Copy binaries from archive stage
COPY --from=archive /bin /usr/local/bin
RUN chmod +x /usr/local/bin/flockadoo

# Create required directories
RUN mkdir -p /root/.config/flockadoo /root/.local/state/flockadoo/log

# Copy scripts
COPY scripts/install.sh scripts/entrypoint.sh /app/
RUN chmod +x /app/install.sh /app/entrypoint.sh

# The running client must not try to replace the image binary.
ENV FLOCKADOO_IN_DOCKER=1

# Set working directory
WORKDIR /app

# Expose ports
EXPOSE 8080

# Set entrypoint
ENTRYPOINT ["/app/entrypoint.sh"]

# Default command
#CMD ["tail", "-f", "/dev/null"]
CMD ["flockadoo", "--config", "/root/.config/flockadoo/config.yaml"] 