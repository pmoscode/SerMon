# Builds the apt repository of the E2E environment (M4-08): apt-ftparchive and gpg.
FROM debian:13@sha256:9c6e381421d0f48d1b8133e573f33c185ee16cbf505eda9c4a8a4791b25ea8a6
RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends apt-utils gnupg >/dev/null && rm -rf /var/lib/apt/lists/*
