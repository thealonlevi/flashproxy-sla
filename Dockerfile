# Builds origin + worker + website into one image; compose/Terraform pick one per
# service. Cross-compiles via buildx (BUILDPLATFORM builder, TARGETARCH output) so
# linux/arm64 (t4g) images build fast on an amd64 CI runner — Go just cross-compiles.
FROM --platform=$BUILDPLATFORM golang:1.25 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -o /out/origin  ./cmd/origin  && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -o /out/worker  ./cmd/worker  && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -o /out/website ./cmd/website

FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /out/origin  /app/origin
COPY --from=build /out/worker  /app/worker
COPY --from=build /out/website /app/website
COPY web    /app/web
COPY config /app/config
# Service command is supplied by the caller (e.g. ["/app/worker", "-config=..."]).
