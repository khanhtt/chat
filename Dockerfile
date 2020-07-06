FROM golang:latest

MAINTAINER Khanh Tong <khanhcntt@gmail.com>



RUN go get github.com/tinode/fcm
RUN go get github.com/tinode/snowflake
RUN go get github.com/aws/aws-sdk-go/aws
RUN go get github.com/aws/aws-sdk-go/service
RUN go get github.com/jmespath/go-jmespath
RUN go get github.com/mitchellh/gox

RUN go get github.com/khanhtt/chat/server && go install --tags dynamodb github.com/khanhtt/chat/server
RUN go get github.com/khanhtt/chat/keygen && go install github.com/khanhtt/chat/keygen
RUN go get github.com/khanhtt/chat/tinode-dynamodb && go install --tags dynamodb github.com/khanhtt/chat/tinode-dynamodb


WORKDIR /app
COPY ./.aws /.aws
COPY entrypoint.sh .
COPY tinode.conf .
COPY tinode-dynamodb/config.json .
COPY tinode-dynamodb/config.json /go/bin
RUN chmod +x entrypoint.sh
COPY tinode.conf /go/bin/
ENTRYPOINT [ "./entrypoint.sh"]

EXPOSE 8080