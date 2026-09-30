# One image per service: docker compose passes the command to build as CMD.
FROM golang:1.25-alpine AS build
ARG CMD
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/service ./cmd/${CMD}

FROM scratch
COPY --from=build /out/service /service
USER 65532:65532
ENTRYPOINT ["/service"]
