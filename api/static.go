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

	// 启动时预读 index.html，避免每次请求都 Open
	indexHTML, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		panic("内嵌前端缺少 index.html: " + err.Error())
	}

	fileServer := http.FileServer(http.FS(distFS))

	serveFile := func(c *gin.Context) {
		// 去掉开头 /，得到相对路径
		path := strings.TrimPrefix(c.Request.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}

		// 尝试打开文件，不存在或是目录则返回 index.html（SPA 路由）
		f, err := distFS.Open(path)
		if err != nil {
			c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
			return
		}
		stat, _ := f.Stat()
		f.Close()
		if stat.IsDir() {
			c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
			return
		}

		// 真实文件：用标准 FileServer（自动处理 Content-Type / ETag / 缓存头）
		fileServer.ServeHTTP(c.Writer, c.Request)
	}

	// 显式注册前端需要的路径，不依赖 NoRoute
	r.GET("/", serveFile)
	r.GET("/favicon.ico", serveFile)
	r.GET("/assets/*filepath", serveFile)

	// 兜底：未匹配到任何路由时返回 index.html（处理其他 SPA 子路由）
	r.NoRoute(serveFile)
}
