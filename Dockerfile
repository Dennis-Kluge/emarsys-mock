# The build stage needs a toolchain; the result needs nothing at all.
FROM golang:1.25 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO off is what makes the scratch stage below possible: the SQLite driver is
# pure Go, so the binary has no dynamic dependencies. -trimpath keeps build
# paths out of the binary so the image is reproducible.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/emarsys-mock ./cmd/emarsys-mock

FROM scratch
COPY --from=build /out/emarsys-mock /emarsys-mock

# Nothing else is copied in. There is no zoneinfo because the binary embeds the
# IANA database via time/tzdata -- without that, Europe/Vienna would fail to
# resolve here and export timestamps would silently come out in UTC. There is no
# CA bundle because the mock makes no outbound TLS calls; if you point
# WEBHOOK_URL at an https endpoint, switch the final stage to
# gcr.io/distroless/static, which carries one.
ENV EMARSYS_MOCK_ADDR=:8080 \
    EMARSYS_MOCK_DB=:memory:

EXPOSE 8080
ENTRYPOINT ["/emarsys-mock"]
