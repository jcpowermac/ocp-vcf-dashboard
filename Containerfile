FROM golang:1.23 AS builder

WORKDIR /workspace

COPY go.mod go.sum ./
COPY vendor/ vendor/
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY static/ static/

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ocp-vcf-dashboard ./cmd/dashboard

FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /workspace/ocp-vcf-dashboard .
COPY --from=builder /workspace/static/ /static/
COPY --from=builder /workspace/pkg/views/ /pkg/views/

USER 65532:65532

ENTRYPOINT ["/ocp-vcf-dashboard"]
