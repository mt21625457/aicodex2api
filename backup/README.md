# backupd 备份进程

`backupd` 是运行在宿主机上的备份执行进程，负责：

- 接收主进程通过 gRPC Unix Socket 发起的备份管理请求
- 执行 PostgreSQL / Redis / Full 备份任务
- 将备份产物可选上传到标准 S3（`aws-sdk-go-v2`）
- 使用 Ent + SQLite 持久化备份配置与任务状态

## 1. 本地构建

```bash
cd backup
go build -o backupd ./cmd/backupd
```

## 2. 本地运行

```bash
cd backup
./backupd \
  -socket-path /tmp/sub2api-backup.sock \
  -sqlite-path /tmp/sub2api-backupd.db \
  -version dev
```

默认参数：

- `-socket-path`: `/tmp/sub2api-backup.sock`
- `-sqlite-path`: `/tmp/sub2api-backupd.db`
- `-version`: `dev`

## 3. 依赖要求

- PostgreSQL 客户端：`pg_dump`
- Redis 客户端：`redis-cli`
- 若使用 `docker_exec` 源模式：`docker`

## 4. 与主进程协作要求

- 主进程固定探测 `/tmp/sub2api-backup.sock`
- 只有探测到该 UDS 且 `Health` 成功时，管理后台“数据管理”功能才会启用
- `backupd` 本身不做业务鉴权，依赖主进程管理员鉴权 + UDS 文件权限

## 5. 生产建议

- 使用 `systemd` 托管进程（参考 `deploy/sub2api-backupd.service`）
- 建议 `backupd` 与 `sub2api` 在同一宿主机运行
- 若 `sub2api` 在 Docker 容器内，需把宿主机 `/tmp/sub2api-backup.sock` 挂载到容器内同路径
