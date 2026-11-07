// Command launcher 是桌面启动器：检查数据库、拉起服务、打开浏览器。
//
// 单独一个 exe 而不是 .bat：bat 会有黑框闪一下、编码还容易乱，
// 双击一个 exe 更像正常软件。
package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

const (
	pgBin     = `E:\PostgreSQL\pgsql\bin`
	pgData    = `E:\PostgreSQL\data`
	serverExe = `bin\ace.exe`
	envFile   = `.env`
	addr      = "127.0.0.1:8090"
	url       = "http://127.0.0.1:8090"

	// createNoWindow：给子进程一个**隐藏的**控制台。
	//
	// 为什么不是 DETACHED_PROCESS：那个是「不分配控制台」，
	// 但 pg_ctl 会用 cmd.exe 包一层去重定向输出，cmd 是控制台程序，
	// 没控制台就自己新建一个 → 弹出一个空的 cmd 窗口。
	// CREATE_NO_WINDOW 则是「有控制台但不显示」，两个问题一起解决：
	// 子进程不挂在启动器控制台上（窗口能正常关闭），也不会新建可见窗口。
	createNoWindow = 0x08000000
)

func main() {
	useUTF8Console()

	fmt.Println("==========================================")
	fmt.Println("  InterviewAce 启动中")
	fmt.Println("==========================================")
	fmt.Println()

	base, err := resolveBase()
	if err != nil {
		fail(err.Error())
	}

	// 1. 数据库
	fmt.Println("[1/3] 检查 PostgreSQL…")
	if err := ensurePostgres(); err != nil {
		fail(err.Error())
	}

	// 2. 服务
	fmt.Println("[2/3] 检查服务…")
	if portOpen(addr) {
		fmt.Println("      服务已在运行，跳过启动")
	} else {
		if err := startServer(base); err != nil {
			fail(err.Error())
		}
		if err := waitPort(addr, 15*time.Second); err != nil {
			fail("服务启动超时，检查 " + filepath.Join(base, envFile) + " 里的配置")
		}
		fmt.Println("      服务已启动")
	}

	// 3. 浏览器
	fmt.Println("[3/3] 打开浏览器…")
	if err := openBrowser(url); err != nil {
		fmt.Println("      自动打开失败，请手动访问 " + url)
	}

	fmt.Println()
	fmt.Println("已就绪：" + url)
	fmt.Println("这个窗口 3 秒后自动关闭，关掉不影响服务。")
	time.Sleep(3 * time.Second)
}

// useUTF8Console 把控制台代码页切成 UTF-8。
//
// 不切的话：Go 输出的是 UTF-8，Windows 控制台默认是 GBK(936)，
// 中文全变乱码。这是 Go 写 Windows 命令行程序的标准坑。
func useUTF8Console() {
	k := syscall.NewLazyDLL("kernel32.dll")
	setOutputCP := k.NewProc("SetConsoleOutputCP")
	setCP := k.NewProc("SetConsoleCP")
	// 65001 = CP_UTF8
	_, _, _ = setOutputCP.Call(65001)
	_, _, _ = setCP.Call(65001)

	setTitle := k.NewProc("SetConsoleTitleW")
	title, err := syscall.UTF16PtrFromString("InterviewAce")
	if err == nil {
		_, _, _ = setTitle.Call(uintptr(unsafe.Pointer(title)))
	}
}

// defaultBase 是编译时注入的项目目录。
//
// 为什么需要：把 exe 拷到桌面时，它旁边没有 bin/ace.exe，
// 光靠 exe 自身位置找不到项目。编译时用 -ldflags 把路径写进来：
//
//	go build -ldflags "-X main.defaultBase=<项目绝对路径>" -o <桌面>/InterviewAce.exe ./cmd/launcher
var defaultBase string

// resolveBase 找到项目目录：先看 exe 旁边，再退回编译时注入的路径。
func resolveBase() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("定位程序位置失败: %v", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	candidates := []string{filepath.Dir(exe)}
	if defaultBase != "" {
		candidates = append(candidates, defaultBase)
	}

	return pickBase(candidates)
}

// pickBase 从候选目录里挑出第一个含 serverExe 的。
// 单独抽出来是为了能测——resolveBase 依赖 os.Executable()，测不了。
func pickBase(candidates []string) (string, error) {
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, serverExe)); err == nil {
			return dir, nil
		}
	}
	return "", fmt.Errorf("找不到项目目录（缺 %s）。\n已尝试：%v", serverExe, candidates)
}

// ensurePostgres 没在跑就拉起来。
//
// 注意：这里刻意不用 CombinedOutput / 管道收集输出。
// pg_ctl start 会把 postgres.exe 作为子进程留下，子进程继承输出管道，
// 管道一直不关闭 → 读取方永远阻塞。表现就是启动器卡在「检查 PostgreSQL…」。
// 把输出写到文件就没有这个问题：文件句柄被继承也不会阻塞谁。
func ensurePostgres() error {
	status := exec.Command(filepath.Join(pgBin, "pg_ctl.exe"), "-D", pgData, "status")
	status.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	status.Stdout = nil // nil = 丢弃，不建管道
	status.Stderr = nil
	if err := status.Run(); err == nil {
		fmt.Println("      数据库已在运行")
		return nil
	}

	// 上次非正常退出会留下 pid 文件，不清掉 pg_ctl 会拒绝启动
	pid := filepath.Join(pgData, "postmaster.pid")
	if _, err := os.Stat(pid); err == nil {
		_ = os.Remove(pid)
	}

	logDir := filepath.Join(filepath.Dir(pgData), "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("创建日志目录失败：%v", err)
	}

	serverLog := filepath.Join(logDir, "server.log")
	launcherLog := filepath.Join(logDir, "launcher_pg.log")

	f, err := os.OpenFile(launcherLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("打开日志文件失败：%v", err)
	}
	defer func() { _ = f.Close() }()

	start := exec.Command(filepath.Join(pgBin, "pg_ctl.exe"),
		"-D", pgData, "-l", serverLog, "-w", "start")
	start.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	start.Stdout = f
	start.Stderr = f

	if err := start.Run(); err != nil {
		return fmt.Errorf("数据库启动失败：%v\n详见 %s 和 %s", err, launcherLog, serverLog)
	}
	fmt.Println("      数据库已启动")
	return nil
}

// startServer 拉起服务进程。
//
// 不 Wait：启动器退出后服务要接着跑。Windows 上父进程退出不会带走子进程。
func startServer(base string) error {
	exe := filepath.Join(base, serverExe)
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("找不到 %s，先执行 go build -o bin/ace.exe ./cmd/server", exe)
	}

	// 服务的日志写文件：detach 之后没有控制台，stdout 会丢
	logDir := filepath.Join(base, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("创建日志目录失败：%v", err)
	}
	lf, err := os.OpenFile(filepath.Join(logDir, "server.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("打开服务日志失败：%v", err)
	}
	defer func() { _ = lf.Close() }()

	cmd := exec.Command(exe, "-env", filepath.Join(base, envFile))
	cmd.Dir = base
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("服务启动失败：%v", err)
	}
	// 不调 cmd.Wait()，让它独立跑
	_ = cmd.Process.Release()
	return nil
}

func portOpen(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// waitPort 等端口就绪，比固定 sleep 稳（机器慢时不会误判失败）。
func waitPort(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if portOpen(addr) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("等待 %s 超时", addr)
}

func openBrowser(u string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
}

func fail(msg string) {
	fmt.Println()
	fmt.Println("[失败] " + msg)
	fmt.Println()
	fmt.Println("按任意键退出…")
	_, _ = fmt.Scanln()
	os.Exit(1)
}
