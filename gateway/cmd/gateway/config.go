package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/mini-maas/gateway/internal/auth"
)

type config struct {
	Listen   string `json:"listen"`
	Metering bool   `json:"metering"` // token计量开关, 开启则需要注入include_usage, 关闭: 限流扣预算但不结算
	Defaults struct {
		MaxCompletionTokens int `json:"max_completion_tokens"` // 默认的最大完成token数
	} `json:"defaults"`
	Keys []keyConfig `json:"keys"`
}

type keyConfig struct {
	Key    string   `json:"key"`
	Tenant string   `json:"tenant"`
	Models []string `json:"models"`
	Quota  struct {
		QPS      float64 `json:"qps"`
		QPSBurst float64 `json:"qps_burst"`
		TPM      float64 `json:"tpm"`
	} `json:"quota"`
}

func loadConfig(path string) (*config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cfg := &config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.Listen = ":8080"
	cfg.Defaults.MaxCompletionTokens = 1024 // 设置默认的最大完成token数

	return cfg, nil
}

func (c *config) identities() map[string]*auth.Identity {
	ids := make(map[string]*auth.Identity, len(c.Keys))
	for _, k := range c.Keys {
		ids[k.Key] = &auth.Identity{
			Key:    k.Key,
			Tenant: k.Tenant,
			Models: k.Models,
			Quota: auth.Quota{
				QPS:      k.Quota.QPS,
				QPSBurst: k.Quota.QPSBurst,
				TPM:      k.Quota.TPM,
			},
		}
	}

	return ids
}
