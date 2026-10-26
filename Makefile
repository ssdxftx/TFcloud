SHELL := /bin/bash
GO ?= go
BIN := tfcloud

.PHONY: help ui build test run migrate doctor docker clean

help:
	@echo "make ui       构建前端到 internal/webui/dist"
	@echo "make build    编译单二进制（内嵌前端）"
	@echo "make test     运行单元测试"
	@echo "make run      本地启动（数据目录 ./data）"
	@echo "make docker   构建镜像并启动 Compose"
	@echo "make doctor   自检配置与数据库"

ui:
	cd ui && pnpm install --frozen-lockfile && pnpm build

build: ui
	CGO_ENABLED=0 $(GO) build -tags embedui -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/tfcloud
	@ls -lh $(BIN)

test:
	$(GO) test ./...

run:
	TF_DATA_DIR=./data $(GO) run ./cmd/tfcloud serve

migrate:
	TF_DATA_DIR=./data $(GO) run ./cmd/tfcloud migrate

doctor:
	TF_DATA_DIR=./data $(GO) run ./cmd/tfcloud doctor

docker:
	docker compose up -d --build

clean:
	rm -rf $(BIN) internal/webui/dist/assets
