# hn-reader

A simple interface for reading the top 10 daily Hacker News articles from [Hacker News Daily](https://www.daemonology.net/hn-daily/)

## Running:

> go run main.go

## Deploying

```
go mod download

CGO_ENABLED=1 GOOS=linux go build -o hn-reader

docker build -t hn-reader .

docker-compose up -d
