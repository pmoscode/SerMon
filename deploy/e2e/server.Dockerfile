# sermon-server for the E2E environment: the binary built by scripts/e2e-demo.sh (UI embedded,
# release key of the run compiled in). Non-root; the state directory belongs to the server user so
# that the named volume inherits the ownership.
FROM debian:13-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2
COPY sermon-server /usr/local/bin/sermon-server
RUN mkdir -p /var/lib/sermon && chown 65532:65532 /var/lib/sermon
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/sermon-server"]
