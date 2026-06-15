# Single image building all three binaries; compose picks one per service.
FROM golang:1.25 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/origin    ./cmd/origin    && \
    CGO_ENABLED=0 go build -trimpath -o /out/collector ./cmd/collector && \
    CGO_ENABLED=0 go build -trimpath -o /out/prober    ./cmd/prober

FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /out/origin    /app/origin
COPY --from=build /out/collector /app/collector
COPY --from=build /out/prober    /app/prober
COPY web    /app/web
COPY config /app/config
# Service command is supplied by docker-compose (e.g. ["/app/collector", ...]).
