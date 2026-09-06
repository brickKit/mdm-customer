# ⚠️ 基底必须带 /bin/sh 加 wget 或 curl（§12.3.7）。
# 平台生成的健康检查是 CMD-SHELL：
#   ["CMD-SHELL", "wget -q --spider http://localhost:8080/healthz || curl -fsS ... || exit 1"]
# FROM scratch / distroless 连 shell 都没有，healthCheck.type: tcp 走的 nc -z 同样是 CMD-SHELL。
# 「在镜像里塞一个静态编译的探针二进制」这条出路不成立——平台只会调 wget/curl 这两个名字。
FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server  ./backend/cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./backend/cmd/migrate

FROM alpine:3.20
RUN apk add --no-cache wget ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/server  /app/server
COPY --from=build /out/migrate /app/migrate
COPY migrations /app/migrations
# ⚠️ 端口不是平台注入的（§13.8.1）：RunStandalone 自己读这份文件的
# deployment.port/extraPorts 来决定监听哪个端口（be-sdk-go 的
# manifest.go）。漏拷这一份，容器启动时会直接报错退出——这是 Task 16
# 对着真实 brickkit up 才核对出来的，计划原文的 Dockerfile 模板没有
# 这一行。
COPY component.yaml /app/component.yaml
EXPOSE 8080 9090
ENTRYPOINT ["/app/server"]
