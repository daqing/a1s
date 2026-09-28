# One fat image with a command override: every A1s process is a subcommand
# of the same binary (api, scheduler, monitor, worker), so a deployment
# picks its role via `docker run a1s <command>` instead of one image per
# process (decision recorded in docs/architecture.md).
FROM golang:1.27-alpine AS builder
WORKDIR /src
COPY . /src
RUN go build -o /out/a1s .

FROM alpine
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /out/a1s /app/a1s
COPY --from=builder /src/db /app/db
ENV AIRWAY_ENV=production
ENV PORT=1905
ENTRYPOINT ["/app/a1s"]
CMD ["api"]
