//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Rect 表示屏幕上的矩形区域（物理像素坐标，已考虑 DPI 缩放）。
type Rect struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Right  int `json:"right"`
	Bottom int `json:"bottom"`
}

// Point 表示屏幕上的一个点（物理像素坐标）。
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Config 是 config.json 的内存结构。
type Config struct {
	NameRegion   Rect    `json:"name_region"`
	OnlineRegion Rect    `json:"online_region"`
	ClickPoints  []Point `json:"click_points"`  // 组1：打招呼按钮的点击点
	ClickPoints2 []Point `json:"click_points2"` // 组2：下一页按钮的点击点
	MaxGreets    int     `json:"max_greets"`    // 本轮最多打多少次招呼，0 = 不限
	TopMost      bool    `json:"topmost"`       // 主窗口是否置顶，默认开启
}

const configFileName = "config.json"

// configPath 返回 config.json 的完整路径。
//
// 正常运行时取 exe 所在目录：exe 放到哪里，配置就跟着到哪里。
// 但 `go run .` 时 exe 落在临时构建目录里，这种情况回退到当前工作目录，
// 保证开发阶段 config.json 仍然写在项目根目录。
func configPath() string {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if !strings.Contains(strings.ToLower(dir), "go-build") {
			return filepath.Join(dir, configFileName)
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, configFileName)
	}
	return configFileName
}

// loadConfig 读取 config.json；文件不存在或损坏时返回零值配置。
func loadConfig() Config {
	var cfg Config
	// 前置默认值：缺省置顶。config.json 里没写、或写了 false 都按"关"处理，
	// 只有显式 false 才会关；新建配置（无该字段）自动就是置顶。
	cfg.TopMost = true
	data, err := os.ReadFile(configPath())
	if err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	if cfg.ClickPoints == nil {
		// 保证 JSON 里始终是 [] 而不是 null
		cfg.ClickPoints = []Point{}
	}
	if cfg.ClickPoints2 == nil {
		cfg.ClickPoints2 = []Point{}
	}
	return cfg
}

// saveConfig 把配置以缩进格式写入 config.json。
func saveConfig(cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0644)
}

// ensureConfigFile 保证 config.json 一定存在：首次运行且文件缺失时写入默认配置。
func ensureConfigFile(cfg Config) {
	if _, err := os.Stat(configPath()); err == nil {
		return
	}
	_ = saveConfig(cfg)
}

// rectIsSet 用「四个值是否全为 0」判定该位置有没有被设置过。
func rectIsSet(r Rect) bool {
	return !(r.Left == 0 && r.Top == 0 && r.Right == 0 && r.Bottom == 0)
}
