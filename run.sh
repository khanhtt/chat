#!/bin/bash
AWS_ACCESS_KEY_ID=$(aws --profile default configure get aws_access_key_id)
AWS_SECRET_ACCESS_KEY=$(aws --profile default configure get aws_secret_access_key)

echo $AWS_ACCESS_KEY_ID
echo $AWS_SECRET_ACCESS_KEY

docker build -t hxro-chat-dev .
docker run -d -p 8080:8080 --name hxro-chat-dev hxro-chat-dev -e AWS_ACCESS_KEY_ID=$AWS_ACCESS_KEY_ID \
   -e AWS_SECRET_ACCESS_KEY=$AWS_SECRET_ACCESS_KEY