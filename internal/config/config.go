// Package config 负责把 .env 文件和进程环境变量读成结构化的配置对象。
//
// 设计原则：
//  1. 真实环境变量优先于 .env 文件（部署时可以直接覆盖，不用改文件）
//  2. 必填项缺失时一次性报出全部缺失项，不是遇到第一个就返回
//  3. 报错信息里绝不带值本身，避免密钥泄漏到日志
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 是应用的全部运行时配置。
type Config struct {
	DB     DBConfig
	LLM    LLMConfig
	Server ServerConfig
	Upload UploadConfig
}

// DBConfig 是 PostgreSQL 连接参数。
type DBConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Name     string
	SSLMode  string
}

// DSN 拼出 pgx 可用的连接串。
// 注意：这个返回值含密码，只能传给数据库驱动，不能打日志。
func (d DBConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, d.Name, d.SSLMode,
	)
}

// LLMConfig 是 OpenAI 兼容接口的参数。
type LLMConfig struct {
	APIKey  string
	BaseURL string
	Model   string
	Timeout time.Duration
	// Thinking 控制是否开启推理模式。默认关闭：
	// 开了会让 temperature 失效、CoT 计入输出 token（价格翻几倍）、
	// 同一输入两次结果差异变大——本项目的评分一致性靠的就是低随机性。
	Thinking  bool
	MaxTokens int
}

// minJWTSecretLen 是 HMAC 密钥的最小长度。
//
// 32 字符约等于 128 bit 熵。低于这个长度，离线暴力破解的成本会降到可接受范围。
const minJWTSecretLen = 32

// insecureJWTSecrets 是绝对不能用的示例值。
//
// 这些值出现在 .env.example 和各类教程里，等于公开的密钥——
// 用它们签发的令牌，任何看过仓库的人都能伪造。
// 启动时直接拒绝，不给「上线再改」留余地：这种「以后再说」从来没有以后。
var insecureJWTSecrets = map[string]bool{
	"dev-secret-change-me-in-production": true,
	"dev-secret":                         true,
	"change-me":                          true,
	"secret":                             true,
	"changeme":                           true,
}

// ServerConfig 是 HTTP 服务的参数。
type ServerConfig struct {
	Port int
	// JWTSecret 用于签发登录令牌。示例值和短于 32 字符的值会被拒绝启动。
	JWTSecret string
}

// UploadConfig 是简历上传的参数。
type UploadConfig struct {
	Dir   string
	MaxMB int
}

// Load 读取 envPath 指向的 .env 文件，再与真实环境变量合并。
// envPath 不存在时不报错（CI 里通常只给真实环境变量），但必填项仍会校验。
func Load(envPath string) (*Config, error) {
	fileVals, err := parseEnvFile(envPath)
	if err != nil {
		return nil, err
	}

	get := func(key, fallback string) string {
		// 真实环境变量优先；空字符串视为未设置，好让 .env 兜底
		if v := os.Getenv(key); v != "" {
			return v
		}
		if v, ok := fileVals[key]; ok && v != "" {
			return v
		}
		return fallback
	}

	cfg := &Config{
		DB: DBConfig{
			Host:     get("DB_HOST", "127.0.0.1"),
			User:     get("DB_USER", "postgres"),
			Password: get("DB_PASSWORD", ""),
			Name:     get("DB_NAME", "mock_interview"),
			SSLMode:  get("DB_SSLMODE", "disable"),
		},
		LLM: LLMConfig{
			APIKey:  get("LLM_API_KEY", ""),
			BaseURL: get("LLM_BASE_URL", "https://api.deepseek.com/v1"),
			Model:   get("LLM_MODEL", "deepseek-flash"),
		},
		Server: ServerConfig{
			JWTSecret: get("JWT_SECRET", ""),
		},
		Upload: UploadConfig{
			Dir: get("UPLOAD_DIR", "./uploads"),
		},
	}

	var problems []string

	cfg.DB.Port, err = parseInt(get("DB_PORT", "5432"), "DB_PORT")
	if err != nil {
		problems = append(problems, err.Error())
	}
	cfg.Server.Port, err = parseInt(get("SERVER_PORT", "8080"), "SERVER_PORT")
	if err != nil {
		problems = append(problems, err.Error())
	}
	cfg.Upload.MaxMB, err = parseInt(get("MAX_UPLOAD_MB", "5"), "MAX_UPLOAD_MB")
	if err != nil {
		problems = append(problems, err.Error())
	}

	// thinking 默认关。解析失败也算配置错误，不静默当 false——
	// 写错成 "ture" 却悄悄按关闭跑，排查起来很费劲。
	if raw := get("LLM_THINKING", "false"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("LLM_THINKING 不是合法布尔值: %q（用 true / false）", raw))
		} else {
			cfg.LLM.Thinking = v
		}
	}

	timeoutSec, err := parseInt(get("LLM_TIMEOUT_SEC", "60"), "LLM_TIMEOUT_SEC")
	if err != nil {
		problems = append(problems, err.Error())
	}
	cfg.LLM.Timeout = time.Duration(timeoutSec) * time.Second

	cfg.LLM.MaxTokens, err = parseInt(get("LLM_MAX_TOKENS", "1024"), "LLM_MAX_TOKENS")
	if err != nil {
		problems = append(problems, err.Error())
	}

	// 必填项：缺了服务起不来，所以直接失败而不是给默认值
	if cfg.DB.Password == "" {
		problems = append(problems, "DB_PASSWORD 未设置")
	}
	if cfg.LLM.APIKey == "" {
		problems = append(problems, "LLM_API_KEY 未设置（请在 .env 里填）")
	}
	if cfg.Server.JWTSecret == "" {
		problems = append(problems, "JWT_SECRET 未设置")
	} else if insecureJWTSecrets[cfg.Server.JWTSecret] {
		problems = append(problems,
			"JWT_SECRET 还是示例值——任何拿到仓库的人都能伪造令牌。\n"+
				"  生成一个：openssl rand -hex 32")
	} else if len(cfg.Server.JWTSecret) < minJWTSecretLen {
		problems = append(problems,
			fmt.Sprintf("JWT_SECRET 太短（%d 字符，至少 %d）——短密钥可被暴力破解",
				len(cfg.Server.JWTSecret), minJWTSecretLen))
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("配置校验失败：\n  - %s", strings.Join(problems, "\n  - "))
	}

	return cfg, nil
}

// parseEnvFile 解析 KEY=VALUE 形式的文件。
// 支持：# 开头整行注释、空行、值两侧的成对引号、行尾的 " #" 注释。
// 不支持：多行值、变量插值、export 前缀——这个项目不需要。
func parseEnvFile(path string) (map[string]string, error) {
	out := make(map[string]string)
	if path == "" {
		return out, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil // 没有 .env 不算错，可能全走环境变量
		}
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}

	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s 第 %d 行格式错误：缺少 '='", path, i+1)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("%s 第 %d 行格式错误：key 为空", path, i+1)
		}

		out[key] = unquote(strings.TrimSpace(val))
	}

	return out, nil
}

// unquote 去掉值两侧成对的单引号或双引号，并处理行尾注释。
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	// 未加引号时，" #" 之后视为注释（值里含 # 的必须加引号）
	if idx := strings.Index(s, " #"); idx >= 0 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}

func parseInt(raw, key string) (int, error) {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s 不是合法整数: %q", key, raw)
	}
	return n, nil
}
