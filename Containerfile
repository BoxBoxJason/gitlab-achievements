FROM docker.io/library/node:24.22.0-alpine3.24@sha256:44fce696f38ba8c987c4db89954051000bbd05ffd8d8659ba15ed4a8602e937f AS web

# The frontend is built first and compiled into the binary below, so an
# image always serves the frontend made for its own API. The lockfile is
# copied alone first so the dependency layer survives source-only changes.
WORKDIR /web

COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY web/ ./
RUN npm run build

FROM docker.io/golang:1.27.3-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build

WORKDIR /app

COPY . .
COPY --from=web /web/dist ./web/dist

ENV GO111MODULE=on \
    CGO_ENABLED=0

RUN apk add --no-cache make git && \
  make build

FROM docker.io/library/alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS security_provider

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
