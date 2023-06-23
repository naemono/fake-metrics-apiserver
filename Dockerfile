# Build the operator binary
FROM docker.io/library/golang:1.20.5 as builder

WORKDIR /go/src/sigs.k8s.io/fake-metrics-apiserver

# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
COPY ["go.mod", "go.sum", "./"]
RUN --mount=type=cache,mode=0755,target=/go/pkg/mod go mod download

# Copy the go source
COPY pkg/    pkg/
COPY main.go main.go

# Build
RUN --mount=type=cache,mode=0755,target=/go/pkg/mod CGO_ENABLED=0 GOOS=linux \
    go build \
    -mod readonly -a \
    -o fake-metrics-apiserver main.go

# Copy the operator binary into a lighter image
FROM gcr.io/distroless/static:nonroot

COPY --from=builder /go/src/sigs.k8s.io/fake-metrics-apiserver/fake-metrics-apiserver /

ENTRYPOINT ["/fake-metrics-apiserver"]
CMD [ "--secure-port", "8443" ]
