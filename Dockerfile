FROM golang:alpine AS build
WORKDIR /go/src/app
COPY . .
RUN go build -o main

FROM alpine
WORKDIR /app
COPY --from=build /go/src/app/main /app/
CMD ["/app/main"]
