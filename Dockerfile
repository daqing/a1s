FROM golang:1.27-alpine AS builder
WORKDIR /app
COPY . /app
RUN go build -o ./bin/app .

FROM alpine
WORKDIR /app
COPY --from=builder /app/bin/app /app
COPY --from=builder /app/db /app/db

ENV AIRWAY_ENV=production
ENV PORT=1905
ENV TZ="Asia/Shanghai"

EXPOSE 1905

CMD ["/app/app"]
