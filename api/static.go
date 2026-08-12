package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed frontend/dist
var embeddedFrontend embed.FS

// serveFrontend 注册前端静态文件路由，支持 SPA
func serveFrontend(r *gin.Engine) {
	distFS, err := fs.Sub(embeddedFrontend, "frontend/dist")
	if err != nil {
		panic("无法加载内嵌前端资源: " + err.Error())
	}

	httpFS := http.FS(distFS)

	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		name := strings.TrimPrefix(path, "/")
		if name == "" {
			name = "index.html"
		}

		// 尝试打开文件
		f, err := distFS.Open(name)
		if err != nil {
			// 文件不存在 → SPA 路由，返回 index.html
			c.FileFromFS("index.html", httpFS)
			return
		}
		f.Close()

		// 文件存在，直接服务
		c.FileFromFS(name, httpFS)
	})
}
