FROM golang:1.25-alpine AS builder

WORKDIR /src
RUN go env -w GO111MODULE=on
RUN go env -w GOPROXY=https://goproxy.cn,direct
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
ARG LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}"

RUN CGO_ENABLED=0 go build -trimpath -ldflags "${LDFLAGS}" -o /bin/nanobus ./cmd/nanobus

FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /bin/nanobus /usr/local/bin/nanobus

WORKDIR /workspace

ENTRYPOINT ["nanobus"]
