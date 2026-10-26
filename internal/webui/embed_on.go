//go:build embedui

package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var embeddedFS embed.FS

// 开启 embedui 构建标签时，前端产物被编进二进制，部署只需一个可执行文件。
var embedded fs.FS = embeddedFS
