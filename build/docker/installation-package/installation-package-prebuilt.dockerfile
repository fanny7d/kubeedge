# The CRI copy workflow executes /bin/sh, sleep, cp and mv inside this image.
# Match the official Ubuntu 18.04 installation-package runtime; never use scratch.
FROM m.daocloud.io/docker.io/library/ubuntu@sha256:f97a5103cca28097326814718e711c9c41b54853c26959d73495e40b1dd608f2
ARG RELEASE_VERSION
ARG SOURCE_COMMIT
LABEL org.opencontainers.image.source="https://github.com/fanny7d/kubeedge" \
      org.opencontainers.image.version="${RELEASE_VERSION}" \
      org.opencontainers.image.revision="${SOURCE_COMMIT}"
COPY --chmod=0755 edgecore /usr/local/bin/edgecore
COPY --chmod=0755 keadm /usr/local/bin/keadm
RUN /bin/sh -ec 'command -v sleep; command -v cp; command -v mv; /usr/local/bin/keadm version'
WORKDIR /etc/kubeedge
CMD ["/usr/local/bin/keadm"]
