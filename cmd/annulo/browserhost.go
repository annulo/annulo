package main

import (
	"net/http"

	"github.com/annulo/annulo/internal/brand"
	"github.com/annulo/annulo/internal/config"
	"github.com/annulo/annulo/internal/extbridge"
)

// runBrowserHost：Chrome 为 Annulo 插件拉起的 native host，把插件接到本机正在跑的 Annulo（地址每次重连前重新读配置）
func runBrowserHost(origin string) error {
	addr := func() string {
		cfg, err := config.Load()
		if err != nil {
			return "127.0.0.1:7799"
		}
		return cfg.LocalAddr()
	}
	return extbridge.RunHost(origin, addr, http.Header{brand.HeaderNew: {"1"}})
}
