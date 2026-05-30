FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG ROLE=prime
RUN CGO_ENABLED=0 go build -o /out/sov-tiered ./examples/chirp/cmd/mesh-tiered/${ROLE}

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/sov-tiered /sov-tiered
ENTRYPOINT ["/sov-tiered"]
