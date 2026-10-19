package config

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// DefaultProfileName 是内置分身的名字。
//
// 它**没有**单独的目录：default 就是正在运行的这套数据（<magicHome>/cortex）。
// 面板编辑 default 时改的就是当前生效的 SOUL.md / USER.md，所见即所得。
const DefaultProfileName = "default"

// ProfileEnvVar 是运行期覆盖"当前分身"的环境变量。
//
// 之所以用环境变量而不是直接改 cfg.Profile：`magic --profile work chat`
// 只是一次性的选择，不应该被后续任何一次 persistConfig 写进 config.json
// 固化下来。持久化的切换走面板 / `magic config set profile`。
const ProfileEnvVar = "GO_MAGIC_PROFILE"

// ActiveProfile 返回本次进程实际生效的分身名。
// 优先级：GO_MAGIC_PROFILE（--profile 翻译而来）> config.json 的 profile > default。
//
// 名字非法（手改 config.json 写进 "../.." 之类）时回退 default：这个值最终会
// 拼进文件路径，绝不能让它带着分隔符到处跑。
func (c *Config) ActiveProfile() string {
	if p := strings.TrimSpace(os.Getenv(ProfileEnvVar)); p != "" {
		if ValidProfileName(p) {
			return p
		}
		return DefaultProfileName
	}
	if c != nil {
		if p := strings.TrimSpace(c.Profile); p != "" {
			if ValidProfileName(p) {
				return p
			}
			return DefaultProfileName
		}
	}
	return DefaultProfileName
}

// windowsReservedNames 是 Windows 上不能作为目录名的设备名。
// 分身名会直接变成目录名，命中它就等于把文件写进黑洞。
var windowsReservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com1": true, "com2": true, "com3": true, "com4": true, "com5": true,
	"com6": true, "com7": true, "com8": true, "com9": true,
	"lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true, "lpt5": true,
	"lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// ValidProfileName 判断 name 能否作为分身名。
//
// 分身名会被拼进文件路径（<magicHome>/profiles/<name>），所以必须保证它是
// **单个、安全的路径段**：拒绝空串、"." / ".."、路径分隔符（含 Windows
// 的反斜杠）、盘符冒号、NUL、首尾空白，以及 Windows 设备名。
// 允许中文等 Unicode 字母（面板是中文的，用户会直接打"工作"）。
func ValidProfileName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if len([]rune(name)) > 64 {
		return false
	}
	if strings.TrimSpace(name) != name {
		return false
	}
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == 0:
			return false
		case unicode.IsLetter(r), unicode.IsDigit(r):
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return !windowsReservedNames[strings.ToLower(name)]
}

// ProfileDir 返回分身的人设目录（SOUL.md / USER.md / USER.json 所在地）。
//
//   - "" 与 "default" → <magicHome>/cortex，即运行时真正读写的目录；
//   - 其它合法名字   → <magicHome>/profiles/<name>。
//
// 名字非法时返回 ""：调用方必须把它当成参数错误（400）处理，绝不能拿去
// 拼路径 —— 这是防目录穿越的最后一道闸，filepath.Join 自己会老老实实
// 按 ".." 往上跳。
func ProfileDir(magicHome, profile string) string {
	if profile == "" || profile == DefaultProfileName {
		return filepath.Join(magicHome, "cortex")
	}
	if !ValidProfileName(profile) {
		return ""
	}
	return filepath.Join(magicHome, "profiles", profile)
}

// SoulPath 返回分身 SOUL.md 的路径；名字非法时返回 ""。
func SoulPath(magicHome, profile string) string {
	dir := ProfileDir(magicHome, profile)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "SOUL.md")
}

// UserMDPath 返回分身 USER.md（人类可读视图）的路径；名字非法时返回 ""。
func UserMDPath(magicHome, profile string) string {
	dir := ProfileDir(magicHome, profile)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "USER.md")
}

// UserJSONPath 返回分身 USER.json（权威存储）的路径；名字非法时返回 ""。
func UserJSONPath(magicHome, profile string) string {
	dir := ProfileDir(magicHome, profile)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "USER.json")
}
