FROM golang:1.27.1-bookworm@sha256:69a7b9788769bec032d238959b61854e9ae87f57be9029ec04e9885fabf99195 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=0.1.0-dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/labrelayd ./cmd/labrelayd && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/labrelay ./cmd/labrelay
RUN mkdir -p /out/tmp && chmod 1777 /out/tmp
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/tmp /tmp
COPY --from=build /out/labrelayd /labrelayd
COPY --from=build /out/labrelay /labrelay
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/labrelayd"]
CMD ["serve"]
