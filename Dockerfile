FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o loadforge-agent ./main.go

FROM alpine:3.19

RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /app/loadforge-agent .

EXPOSE 8081

ENV AGENT_PORT=8081

ENTRYPOINT ["./loadforge-agent"]
