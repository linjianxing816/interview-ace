package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEnv 在临时目录写一个 .env 文件并返回路径。
func writeEnv(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("写临时 .env 失败: %v", err)
	}
	return p
}

// clearEnv 把所有相关环境变量清空，保证测试不受宿主机影响。
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME", "DB_SSLMODE",
		"LLM_API_KEY", "LLM_BASE_URL", "LLM_MODEL",
		"SERVER_PORT", "JWT_SECRET", "UPLOAD_DIR", "MAX_UPLOAD_MB",
	} {
		t.Setenv(k, "")
	}
}

const validEnv = `
# 注释行应被忽略
DB_HOST=127.0.0.1
DB_PORT=5432
DB_USER=postgres
DB_PASSWORD=secret
DB_NAME=mock_interview

LLM_API_KEY="sk-test-123"
LLM_MODEL=deepseek-flash

SERVER_PORT=8080
JWT_SECRET=test-only-0123456789abcdef0123456789abcdef
`

func TestLoad_正常解析(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(writeEnv(t, validEnv))
	if err != nil {
		t.Fatalf("期望成功，实际报错: %v", err)
	}

	cases := []struct {
		name string
		got  any
		want any
	}{
		{"DB.Host", cfg.DB.Host, "127.0.0.1"},
		{"DB.Port", cfg.DB.Port, 5432},
		{"DB.Password", cfg.DB.Password, "secret"},
		{"LLM.APIKey（双引号应被剥掉）", cfg.LLM.APIKey, "sk-test-123"},
		{"LLM.Model", cfg.LLM.Model, "deepseek-flash"},
		{"Server.Port", cfg.Server.Port, 8080},
		{"Upload.MaxMB（缺省值）", cfg.Upload.MaxMB, 5},
		{"Upload.Dir（缺省值）", cfg.Upload.Dir, "./uploads"},
		{"DB.SSLMode（缺省值）", cfg.DB.SSLMode, "disable"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v，期望 %v", c.name, c.got, c.want)
		}
	}
}

func TestLoad_环境变量优先于文件(t *testing.T) {
	clearEnv(t)
	t.Setenv("SERVER_PORT", "9999")

	cfg, err := Load(writeEnv(t, validEnv))
	if err != nil {
		t.Fatalf("期望成功，实际报错: %v", err)
	}
	if cfg.Server.Port != 9999 {
		t.Errorf("环境变量应覆盖 .env，实际拿到 %d，期望 9999", cfg.Server.Port)
	}
}

func TestLoad_必填项缺失时一次报全(t *testing.T) {
	clearEnv(t)
	// 只给密码，缺 API key 和 JWT
	_, err := Load(writeEnv(t, "DB_PASSWORD=secret\n"))
	if err == nil {
		t.Fatal("期望报错，实际成功")
	}

	msg := err.Error()
	for _, want := range []string{"LLM_API_KEY", "JWT_SECRET"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息应包含 %q，实际为:\n%s", want, msg)
		}
	}
	// 已设置的不应出现在报错里
	if strings.Contains(msg, "DB_PASSWORD") {
		t.Errorf("DB_PASSWORD 已设置，不该出现在报错里:\n%s", msg)
	}
}

func TestLoad_文件不存在不报错但校验照旧(t *testing.T) {
	clearEnv(t)
	_, err := Load(filepath.Join(t.TempDir(), "not-exists.env"))
	if err == nil {
		t.Fatal("文件缺失时应继续校验必填项并报错")
	}
	if !strings.Contains(err.Error(), "LLM_API_KEY") {
		t.Errorf("期望报必填项缺失，实际: %v", err)
	}
}

func TestLoad_端口不是数字时报错(t *testing.T) {
	clearEnv(t)
	body := validEnv + "MAX_UPLOAD_MB=abc\n"
	_, err := Load(writeEnv(t, body))
	if err == nil {
		t.Fatal("期望报错，实际成功")
	}
	if !strings.Contains(err.Error(), "MAX_UPLOAD_MB") {
		t.Errorf("期望提到 MAX_UPLOAD_MB，实际: %v", err)
	}
}

func TestParseEnvFile_格式错误行(t *testing.T) {
	_, err := parseEnvFile(writeEnv(t, "GOOD=1\n这一行没有等号\n"))
	if err == nil {
		t.Fatal("期望报错，实际成功")
	}
	if !strings.Contains(err.Error(), "第 2 行") {
		t.Errorf("期望指出行号，实际: %v", err)
	}
}

func TestUnquote_行尾注释与引号(t *testing.T) {
	cases := []struct{ in, want string }{
		{`plain`, "plain"},
		{`"quoted"`, "quoted"},
		{`'single'`, "single"},
		{`value # 这是注释`, "value"},
		{`"含 # 的值"`, "含 # 的值"},
		{`""`, ""},
	}
	for _, c := range cases {
		if got := unquote(c.in); got != c.want {
			t.Errorf("unquote(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestDBConfig_DSN(t *testing.T) {
	d := DBConfig{Host: "127.0.0.1", Port: 5432, User: "postgres", Password: "p", Name: "mock_interview", SSLMode: "disable"}
	want := "host=127.0.0.1 port=5432 user=postgres password=p dbname=mock_interview sslmode=disable"
	if got := d.DSN(); got != want {
		t.Errorf("DSN() = %q\n期望 %q", got, want)
	}
}

// TestLoad_JWT密钥为示例值时拒绝启动 是安全底线。
//
// 示例值（dev-secret-change-me-in-production 这类）出现在 .env.example 和教程里，
// 等于公开密钥——用它签发的令牌，任何看过仓库的人都能伪造。
// 不拒绝的话，「上线再改」这句话从来没有兑现过。
func TestLoad_JWT密钥为示例值时拒绝启动(t *testing.T) {
	clearEnv(t)

	cases := []string{
		"dev-secret-change-me-in-production",
		"dev-secret",
		"change-me",
		"secret",
	}
	for _, bad := range cases {
		t.Run(bad, func(t *testing.T) {
			body := strings.Replace(validEnv,
				"JWT_SECRET=test-only-0123456789abcdef0123456789abcdef",
				"JWT_SECRET="+bad, 1)

			_, err := Load(writeEnv(t, body))
			if err == nil {
				t.Fatalf("示例值 %q 应被拒绝", bad)
			}
			if !strings.Contains(err.Error(), "示例值") {
				t.Errorf("错误信息应说明是示例值问题: %v", err)
			}
			// 错误信息要给出解决办法，否则用户只能干瞪眼
			if !strings.Contains(err.Error(), "openssl") {
				t.Errorf("错误信息应给出生成命令: %v", err)
			}
		})
	}
}

func TestLoad_JWT密钥太短时拒绝启动(t *testing.T) {
	clearEnv(t)

	body := strings.Replace(validEnv,
		"JWT_SECRET=test-only-0123456789abcdef0123456789abcdef",
		"JWT_SECRET=short-but-not-example", 1)

	_, err := Load(writeEnv(t, body))
	if err == nil {
		t.Fatal("23 字符的密钥应被拒绝")
	}
	if !strings.Contains(err.Error(), "太短") {
		t.Errorf("错误信息应说明长度问题: %v", err)
	}
}

func TestLoad_JWT密钥长度边界(t *testing.T) {
	clearEnv(t)

	// 正好 32 字符应该通过
	body := strings.Replace(validEnv,
		"JWT_SECRET=test-only-0123456789abcdef0123456789abcdef",
		"JWT_SECRET=0123456789abcdef0123456789abcdef", 1)

	if _, err := Load(writeEnv(t, body)); err != nil {
		t.Fatalf("32 字符应通过，实际: %v", err)
	}

	// 31 字符应该被拒
	body = strings.Replace(validEnv,
		"JWT_SECRET=test-only-0123456789abcdef0123456789abcdef",
		"JWT_SECRET=0123456789abcdef0123456789abcde", 1)

	if _, err := Load(writeEnv(t, body)); err == nil {
		t.Fatal("31 字符应被拒绝")
	}
}
