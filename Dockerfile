# syntax=docker/dockerfile:1

##
## Build stage.
##
FROM golang:1.20-alpine AS build

## Install required libraries and tools
RUN apk add protoc grpc protobuf build-base git
RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.28
RUN go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.2
RUN go install github.com/favadi/protoc-go-inject-tag@latest
ENV GO111MODULE=on

WORKDIR /app


# Download dependencies
COPY go.mod ./
COPY go.sum ./
RUN go mod download

# Copy app
COPY . .

# Generate protobuf files
RUN go generate

# build the actual binary
RUN GOOS=linux GOARCH=amd64 go build -ldflags "-linkmode external -s -w -extldflags '-static'" -trimpath -o entry

##
## Deploy stage
##
FROM scratch

# copy the prebuilt image
WORKDIR /
COPY --from=build /app/entry /entry

# expose default listen port
EXPOSE 9999

# set app as startup app
ENTRYPOINT ["/entry"]
