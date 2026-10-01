package auth

import "errors"

var ErrorKeyNotFound = errors.New("key not found")

// Quota是按api key维度的限流配额,与具体模型无关
type Quota struct {
	QPS      float64 `json:"qps"`       // 每秒请求数
	QPSBurst float64 `json:"qps_burst"` // 每秒请求数的突发容量, 瞬时脉冲
	TPM      float64 `json:"tpm"`       // 每分钟token数, 注意每秒补充数据应该是TPM / 60
}

type Identity struct {
	Key    string   `json:"key"`    // api_key, 敏感信息,不要写入日志或者暴露给前端
	Tenant string   `json:"tenant"` // 租户名: 日志/指标/计费使用
	Models []string `json:"models"` // 允许访问的模型列表
	Quota  Quota    `json:"quota"`  // api key的限流配额
}

// 定义KeyStore的接口
// TODO: 目前只有读取, 可以定义在这里, 需要考虑未来用户在平台注册大量api key的场景, 这时候它应该是一个单独的服务或者领域
type KeyStore interface {
	// Get retrieves the value associated with the given key.
	Get(key string) (*Identity, error)
}
