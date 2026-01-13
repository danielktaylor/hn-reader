FROM debian:trixie-slim

# Install SQLite runtime library and ca-certificates
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    libsqlite3-0 \
    ca-certificates && \
    rm -rf /var/lib/apt/lists/*

# Create app directory
WORKDIR /app

# Copy pre-built binary
COPY hn-reader /app/hn-reader

# Copy templates directory
COPY templates /app/templates

# Ensure binary is executable
RUN chmod +x /app/hn-reader

# Expose port
EXPOSE 8080

# Set environment variable for port (optional, defaults to 8080)
ENV PORT=8080

# Run the application
CMD ["/app/hn-reader"]
