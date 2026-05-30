FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG ROLE=gateway
RUN CGO_ENABLED=0 go build -o /out/sov-mesh ./examples/chirp/cmd/mesh/${ROLE}

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/sov-mesh /sov-mesh
ENTRYPOINT ["/sov-mesh"]
