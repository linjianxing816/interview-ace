package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeProject 造一个假项目目录：有 bin/ace.exe 就算「是项目」。
func makeProject(t *testing.T, root string, withServer bool) string {
	t.Helper()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if withServer {
		if err := os.WriteFile(filepath.Join(bin, "ace.exe"), []byte("x"), 0o644); err != nil {
			t.Fatalf("写文件失败: %v", err)
		}
	}
	return root
}

func TestPickBase_取第一个含服务程序的目录(t *testing.T) {
	notProject := makeProject(t, t.TempDir(), false)
	project := makeProject(t, t.TempDir(), true)

	got, err := pickBase([]string{notProject, project})
	if err != nil {
		t.Fatalf("应该找到项目，实际报错: %v", err)
	}
	if got != project {
		t.Errorf("got = %q，期望 %q", got, project)
	}
}

// TestPickBase_桌面副本靠注入路径兜底 是桌面版的命门：
// exe 在桌面时旁边没有 bin/ace.exe，必须靠编译时注入的 defaultBase。
func TestPickBase_桌面副本靠注入路径兜底(t *testing.T) {
	desktop := t.TempDir() // 桌面：没有 bin/ace.exe
	project := makeProject(t, t.TempDir(), true)

	got, err := pickBase([]string{desktop, project})
	if err != nil {
		t.Fatalf("应该退回到注入的路径: %v", err)
	}
	if got != project {
		t.Errorf("got = %q，期望退回 %q", got, project)
	}
}

func TestPickBase_都找不到时报错并列出候选(t *testing.T) {
	a := makeProject(t, t.TempDir(), false)
	b := makeProject(t, t.TempDir(), false)

	_, err := pickBase([]string{a, b})
	if err == nil {
		t.Fatal("应该报错")
	}
	// 报错里要带上试过哪些目录，不然用户没法排查
	if !strings.Contains(err.Error(), a) || !strings.Contains(err.Error(), b) {
		t.Errorf("错误信息应列出候选目录: %v", err)
	}
}

func TestPickBase_跳过空字符串(t *testing.T) {
	project := makeProject(t, t.TempDir(), true)

	got, err := pickBase([]string{"", project})
	if err != nil {
		t.Fatalf("空路径应被跳过: %v", err)
	}
	if got != project {
		t.Errorf("got = %q", got)
	}
}

func TestPickBase_没有候选时报错(t *testing.T) {
	if _, err := pickBase(nil); err == nil {
		t.Fatal("没有候选应该报错")
	}
}
