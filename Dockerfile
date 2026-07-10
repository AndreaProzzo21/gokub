# Specifichiamo la piattaforma di build
FROM --platform=$BUILDPLATFORM golang:alpine AS builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY main.go .

# Preleviamo l'architettura target (amd64 o arm64) passata da buildx
ARG TARGETARCH

# Passiamo TARGETARCH a GOARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -ldflags="-s -w" -a -installsuffix cgo -o gokub .

RUN apk --no-cache add ca-certificates

FROM scratch
WORKDIR /
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/gokub /gokub

CMD ["/gokub"]
