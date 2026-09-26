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

	// QuickGroups 是「快捷回复」页面的点击点，固定 8 组，与上面的打招呼点击点完全独立。
	// 它是纯坐标：不做任何识别与判断，只按采集的点依次/组合点击（自动化后续实现）。
	// 老配置里只有 1～6 组也能直接兼容：数组按 quickGroupCount 定长，
	// 反序列化时读不到的第 7、8 组会保持零值（空），loadConfig 里再补成非 nil 空切片。
	QuickGroups [quickGroupCount][]Point `json:"quick_groups"`

	// QuickIntervals 是旧版「快捷回复」页各组的单值点击间隔（毫秒）。
	// 现已改为随机区间（下面的 Min/Max），此字段仅保留用于从老 config.json 迁移，不再写入新逻辑。
	QuickIntervals [quickGroupCount]int `json:"quick_intervals,omitempty"`

	// QuickIntervalMin / QuickIntervalMax 是「快捷回复」页 8 组各自的随机间隔区间（毫秒）：
	// 每点完一下，在 [Min, Max] 之间随机等待再点下一组，避免固定节奏被系统检测。
	// 与 QuickGroups 一一对应（下标 0 起）。0/留空表示用默认值 quickIntervalDefaultMs；
	// Max 缺省（或小于 Min）时按 Min 处理，等价于「固定等 Min 毫秒」。
	QuickIntervalMin [quickGroupCount]int `json:"quick_interval_min"`
	QuickIntervalMax [quickGroupCount]int `json:"quick_interval_max"`

	// QuickPauseMinMinutes / QuickPauseMaxMinutes 是「快捷回复」页轮流点击的批间休息区间（分钟）：
	// 每批跑完随机 15～25 轮后，在闭区间 [Min, Max] 内随机挑一个整数分钟休息，休息完再开下一批。
	// 两项都填 0（或留空）表示不休息；Max 小于 Min 时按 Min 处理；上限 quickPauseMaxMinutes 分钟。
	QuickPauseMinMinutes int `json:"quick_pause_min_minutes"`
	QuickPauseMaxMinutes int `json:"quick_pause_max_minutes"`
}

// quickGroupCount 是「快捷回复」页固定提供的组数。
const quickGroupCount = 8

// 快捷回复页每组的点击间隔（毫秒）：
// quickIntervalDefaultMs 是输入框留空 / 填 0 / 配置缺失时的兜底值；
// quickIntervalMaxMs 是上限，防止手滑输个天文数字导致点了之后要等半分钟。
const (
	quickIntervalDefaultMs = 500
	quickIntervalMaxMs     = 60000
)

// 批间休息相关常量：
// quickBatchRoundMin / quickBatchRoundMax 是每批目标轮数的随机区间（含端点）；
// quickPauseMaxMinutes 是批间休息分钟数的上限，防止手滑输个天文数字导致永久卡在休息里。
const (
	quickBatchRoundMin   = 15
	quickBatchRoundMax   = 25
	quickPauseMaxMinutes = 1440
)

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
	// 快捷回复的 8 组：保证每格都是非 nil 切片（JSON 里始终是 []，不是 null）。
	// 老配置只写了 1～6 组也没关系，读不到的第 7、8 组在这里补成空切片。
	for i := range cfg.QuickGroups {
		if cfg.QuickGroups[i] == nil {
			cfg.QuickGroups[i] = []Point{}
		}
	}
	// 迁移：老配置只有单值 quick_intervals，把它当成「固定区间」搬到 Min=Max，
	// 这样升级后行为不变（仍是固定间隔），用户想随机再自己把 Max 调大即可。
	for i := range cfg.QuickIntervals {
		if cfg.QuickIntervalMin[i] == 0 && cfg.QuickIntervalMax[i] == 0 && cfg.QuickIntervals[i] > 0 {
			cfg.QuickIntervalMin[i] = cfg.QuickIntervals[i]
			cfg.QuickIntervalMax[i] = cfg.QuickIntervals[i]
		}
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
