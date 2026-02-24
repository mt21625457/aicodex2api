.PHONY: build build-backend build-frontend build-backupd test test-backend test-frontend test-backupd secret-scan

# 一键编译前后端
build: build-backend build-frontend

# 编译后端（复用 backend/Makefile）
build-backend:
	@$(MAKE) -C backend build

# 编译前端（需要已安装依赖）
build-frontend:
	@pnpm --dir frontend run build

# 编译 backupd（宿主机备份进程）
build-backupd:
	@cd backup && go build -o backupd ./cmd/backupd

# 运行测试（后端 + 前端）
test: test-backend test-frontend

test-backend:
	@$(MAKE) -C backend test

test-frontend:
	@pnpm --dir frontend run lint:check
	@pnpm --dir frontend run typecheck

test-backupd:
	@cd backup && go test ./...

secret-scan:
	@python3 tools/secret_scan.py
