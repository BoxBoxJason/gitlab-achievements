FROM docker.io/golang:1.27.1-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS build

WORKDIR /app

COPY . .

ENV GO111MODULE=on \
    CGO_ENABLED=0

RUN apk add --no-cache make git && \
  make build

FROM docker.io/library/alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS security_provider

# The final image has no package manager, so everything the binary needs from
# a distribution is assembled here: an unprivileged account to run as, and the
# CA bundle every HTTPS call to GitLab is verified against.
#
# The UID/GID are pinned rather than left to adduser, because Kubernetes
# refuses to start a container with runAsNonRoot when the image's USER is a
# name it cannot resolve to a number.
RUN apk add --no-cache ca-certificates \
    && addgroup -g 1000 -S gitlab-achievements \
    && adduser -u 1000 -S -G gitlab-achievements gitlab-achievements

FROM scratch

COPY --from=security_provider /etc/passwd /etc/passwd
COPY --from=security_provider /etc/group /etc/group
COPY --from=security_provider /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

USER 1000:1000

COPY --from=build /app/bin/gitlab-achievements /usr/local/bin/gitlab-achievements

ENTRYPOINT [ "/usr/local/bin/gitlab-achievements" ]
