// 前端 UI 资源嵌入（Phase 3）。
//
// 前端为独立工程（见 web/ 目录，Vite 构建），`npm run build` 产物输出到
// web/dist 并提交入库——Go 编译不依赖 Node 环境。
// 此处通过 go:embed 将产物嵌入二进制，运行期零外部文件依赖。
package main

import (
	"embed"
	"io/fs"
)

//go:embed all:web/dist
var distFS embed.FS

// embeddedUI 返回以 web/dist 为根的只读文件系统。
func embeddedUI() (fs.FS, error) {
	return fs.Sub(distFS, "web/dist")
}
