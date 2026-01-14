# hn-reader

A simple interface for reading the top 10 daily Hacker News articles from [Hacker News Daily](https://www.daemonology.net/hn-daily/). Helps you keep track what you have vs have not read.

<img width="533" height="328" alt="Screenshot of main UI" src="https://github.com/user-attachments/assets/2a72f4f4-f0d2-42fb-bcdb-b2a687680495" />

## Running:

> go run main.go

## Deploying

```
go mod download

CGO_ENABLED=1 GOOS=linux go build -o hn-reader

docker build -t hn-reader .

docker compose up -d
```
