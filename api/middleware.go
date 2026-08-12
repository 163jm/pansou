package api

import (
	"strings"

	"github.com/gin-gonic/gin"
	"pansou/config"
	"pansou/util"
)

// CORSMiddleware 跨域中间件
func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")
		
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		
		c.Next()
	}
}

// AuthMiddleware JWT认证中间件
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 如果未启用认证，直接放行
		if !config.AppConfig.AuthEnabled {
			c.Next()
			return
		}

		// 非 /api 路径均为前端静态文件，直接放行
		path := c.Request.URL.Path
		if !strings.HasPrefix(path, "/api") {
			c.Next()
			return
		}

		// 定义公开的 API 接口（不需要认证）
		publicPaths := []string{
			"/api/auth/login",
			"/api/auth/logout",
			"/api/health",
		}

		// 检查当前路径是否是公开接口
		for _, p := range publicPaths {
			if strings.HasPrefix(path, p) {
				c.Next()
				return
			}
		}

		// 获取Authorization头
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(401, gin.H{
				"error": "未授权：缺少认证令牌",
				"code":  "AUTH_TOKEN_MISSING",
			})
			c.Abort()
			return
		}

		// 解析Bearer token
		const bearerPrefix = "Bearer "
		if !strings.HasPrefix(authHeader, bearerPrefix) {
			c.JSON(401, gin.H{
				"error": "未授权：令牌格式错误",
				"code":  "AUTH_TOKEN_INVALID_FORMAT",
			})
			c.Abort()
			return
		}

		tokenString := strings.TrimPrefix(authHeader, bearerPrefix)

		// 验证token
		claims, err := util.ValidateToken(tokenString, config.AppConfig.AuthJWTSecret)
		if err != nil {
			c.JSON(401, gin.H{
				"error": "未授权：令牌无效或已过期",
				"code":  "AUTH_TOKEN_INVALID",
			})
			c.Abort()
			return
		}

		// 将用户信息存入上下文，供后续处理使用
		c.Set("username", claims.Username)
		c.Next()
	}
} 