FROM golang:latest

MAINTAINER Khanh Tong <khanhcntt@gmail.com>



RUN go get github.com/khanhtt/chat/server
RUN go get github.com/tinode/fcm
RUN go get github.com/tinode/snowflake
RUN go get github.com/aws/aws-sdk-go/aws
RUN go get github.com/aws/aws-sdk-go/service
RUN go get github.com/jmespath/go-jmespath
RUN go get github.com/mitchellh/gox

WORKDIR /app
RUN gox -osarch="linux/amd64" -tags dynamodb github.com/khanhtt/chat/server

COPY ./server/tinode-dynamodb.conf ./tinode.conf
ENTRYPOINT [ "./server_linux_amd64"]

EXPOSE 8080