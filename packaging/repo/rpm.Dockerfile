# Builds rpm repositories: of the agent packages (scripts/build-repos.sh, M4-20) and of the E2E
# environment (deploy/e2e/repos, M4-08): createrepo_c, modifyrepo_c and gpg.
FROM rockylinux:9@sha256:03303863808e9cd79f0c58c833db3e5f9cde9f2abe16804a267b05dc54af86e4
RUN dnf install -y -q createrepo_c gnupg2 >/dev/null && dnf clean all >/dev/null
