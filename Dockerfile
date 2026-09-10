# The build stage needs a toolchain; the result does not need anything at all.
FROM golang:1.25 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO off is what makes the scratch stage below possible: the SQLite driver is
# pure Go, so the binary has no dynamic dependencies.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/emarsys-mock ./cmd/emarsys-mock

FROM scratch
COPY --from=build /out/emarsys-mock /emarsys-mock
# No zoneinfo and no CA bundle are copied in: the binary embeds the IANA
# database via time/tzdata, and the mock makes no outbound TLS calls except an
# optional webhook, which is why the default is an in-memory database and a
# plain HTTP listener.
ENV EMARSYS_MOCK_ADDR=:8080 \
    EMARSYS_MOCK_DB=:memory:
EXPOSE 8080
ENTRYPOINT ["/emarsys-mock"]
