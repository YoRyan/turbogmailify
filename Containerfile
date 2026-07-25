FROM golang:1-alpine AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY . .
RUN go mod download
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -a -installsuffix cgo -o ./out/turbogmailify .

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /src/out/turbogmailify /main
USER 10001
ENTRYPOINT ["/main"]
CMD ["/turbogmailify.conf"]