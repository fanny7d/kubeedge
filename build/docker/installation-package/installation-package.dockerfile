FROM kubeedge/build-tools:1.23.12-ke1 AS builder
WORKDIR /work
ADD . .
RUN make WHAT="edgecore keadm" BUILD_WITH_CONTAINER=false

FROM ubuntu:18.04
COPY --from=builder /work/_output/local/bin/edgecore /usr/local/bin/edgecore
COPY --from=builder /work/_output/local/bin/keadm /usr/local/bin/keadm

WORKDIR /etc/kubeedge
# Custom image can add more content here.
# e.g. config
