# Stage 1: Build
FROM golang:alpine AS builder
WORKDIR /app

# Copiamo i file del modulo e scarichiamo le dipendenze
COPY go.mod go.sum ./
RUN go mod download

# Copiamo il codice sorgente
COPY main.go .

# Compilazione statica, ottimizzata e senza simboli di debug (-s -w)
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -a -installsuffix cgo -o gokub .

# Installiamo i certificati radice
RUN apk --no-cache add ca-certificates

# Stage 2: Runtime (Immagine Scratch da 0 Byte)
FROM scratch
WORKDIR /
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/gokub /gokub

# Esecuzione
CMD ["/gokub"]
