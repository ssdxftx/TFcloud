//go:build !embedui

package webui

import "io/fs"

// 默认构建不带前端产物，运行时从磁盘目录读取（见 Handler 的候选目录）。
// 正式构建使用 -tags embedui，见 Dockerfile 与 Makefile。
var embedded fs.FS
