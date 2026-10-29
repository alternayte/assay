FROM --platform=$BUILDPLATFORM oven/bun:1.4.3 AS web
WORKDIR /src/web
COPY web/package.json web/bun.lock ./
RUN bun install --frozen-lockfile
COPY web/ ./
RUN bun run build

FROM --platform=$BUILDPLATFORM golang:1.26.2-bookworm AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/assay ./cmd/assay

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/assay /usr/local/bin/assay
USER nonroot:nonroot
EXPOSE 8080
ENV ASSAY_LISTEN=:8080
ENTRYPOINT ["/usr/local/bin/assay"]
CMD ["serve"]
