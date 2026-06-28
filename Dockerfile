FROM golang:alpine AS builder

COPY . .

RUN go build -o lbryd

FROM alpine

USER 1000

COPY --from=builder lbryd .

EXPOSE 4444/udp
EXPOSE 5279
EXPOSE 5280
EXPOSE 5566
EXPOSE 5567

ENTRYPOINT ["lbryd"]
