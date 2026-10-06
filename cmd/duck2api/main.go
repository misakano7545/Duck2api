package main

import (
	"aurora/internal/initialize"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/gin-gonic/gin"

	"github.com/acheong08/endless"
)

// loadConfigFile 把 config.json 当作「环境变量默认值」读进来: 只回填环境里还没有的键。
// 优先级 真实环境变量 > config.json —— 文件放非敏感项, 敏感项(Authorization/PROXY_URL)
// 在部署面板里用环境变量覆盖即可。
// 键名就是环境变量名, 不另立一套命名, 也不做 section 映射。
// ponytail: 路径固定 ./config.json, 要换路径再加 -config 参数。
func loadConfigFile(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return // 没这个文件是正常情况(Docker/Koyeb 全靠环境变量)
	}
	var kv map[string]any
	if err := json.Unmarshal(raw, &kv); err != nil {
		log.Fatalf("解析 %s 失败: %v", path, err)
	}
	for key, val := range kv {
		if _, exists := os.LookupEnv(key); exists {
			continue // 环境变量优先
		}
		switch v := val.(type) {
		case string:
			_ = os.Setenv(key, v)
		case float64, bool:
			_ = os.Setenv(key, fmt.Sprint(v)) // 8080 这种 JSON 数字 -> "8080"
		default:
			log.Printf("config: 跳过 %s (只支持字符串/数字/布尔)", key)
		}
	}
}

func main() {
	loadConfigFile("config.json")
	gin.SetMode(gin.ReleaseMode)
	router := initialize.RegisterRouter()
	host := os.Getenv("SERVER_HOST")
	port := os.Getenv("SERVER_PORT")
	tlsCert := os.Getenv("TLS_CERT")
	tlsKey := os.Getenv("TLS_KEY")

	if host == "" {
		host = "0.0.0.0"
	}
	if port == "" {
		port = os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
	}

	if tlsCert != "" && tlsKey != "" {
		_ = endless.ListenAndServeTLS(host+":"+port, tlsCert, tlsKey, router)
	} else {
		_ = endless.ListenAndServe(host+":"+port, router)
	}
}
