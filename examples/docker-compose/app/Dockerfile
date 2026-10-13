# The example application: a static Go binary on an empty base image.
FROM golang:1.27.1 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -o /app .

FROM scratch
COPY --from=build /app /app
USER 65532:65532
ENTRYPOINT ["/app"]
