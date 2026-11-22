package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/term"

	"simplewebplayer/internal/nas"
	"simplewebplayer/internal/player"
)

//go:embed web
var embedded embed.FS

var buildVersion = "dev"

func routes(api http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	assets, _ := fs.Sub(embedded, "web")
	serve := http.FileServerFS(assets)
	mux.Handle("GET /{$}", serve)
	mux.HandleFunc("GET /assets/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !fs.ValidPath(name) || name == "." || name == "index.html" {
			http.NotFound(w, r)
			return
		}
		info, err := fs.Stat(assets, name)
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		clone := r.Clone(r.Context())
		clone.URL.Path, clone.URL.RawPath = "/"+name, ""
		serve.ServeHTTP(w, clone)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; media-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func initAdmin(app *player.App, username string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return errors.New("初始化必须在交互终端执行，请使用 SSH 的交互式终端")
	}
	prompt := func(hint string) (string, error) {
		fmt.Fprint(os.Stderr, hint)
		pw, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		defer clear(pw)
		return string(pw), err
	}
	password, err := prompt("管理员密码（至少 10 字符）: ")
	if err != nil {
		return err
	}
	confirm, err := prompt("再次输入密码: ")
	if err != nil {
		return err
	}
	if password != confirm {
		return errors.New("两次密码不一致")
	}
	if err := app.InitAdmin(username, password); err != nil {
		return err
	}
	fmt.Println("管理员已创建。")
	return nil
}

func serve(app *player.App, address string) error {
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler: routes(app), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		IdleTimeout: time.Minute, MaxHeaderBytes: 32 * 1024,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	log.Printf("在线播放服务已监听 %s", ln.Addr())
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		grace, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if err := srv.Shutdown(grace); err != nil {
			_ = srv.Close()
		}
		return nil
	}
}

func run(args []string) error {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Println(buildVersion)
		return nil
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Println("用法: simplewebplayer init-admin [--username admin] [--if-needed] | simplewebplayer serve --host 127.0.0.1 --port 8080")
		return nil
	}
	dataDir := os.Getenv("PLAYER_DATA_DIR")
	if dataDir == "" {
		dataDir = "data"
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	var address, user string
	var ifNeeded bool
	switch args[0] {
	case "init-admin":
		flags.StringVar(&user, "username", "admin", "初始管理员用户名")
		flags.BoolVar(&ifNeeded, "if-needed", false, "已有管理员时直接退出")
	case "serve":
		host := flags.String("host", "127.0.0.1", "监听地址")
		port := flags.Int("port", 8080, "监听端口")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *host == "" || *port < 1 || *port > 65535 {
			return errors.New("监听参数无效")
		}
		address = net.JoinHostPort(*host, strconv.Itoa(*port))
	default:
		return fmt.Errorf("未知命令 %q；请查看 --help", args[0])
	}
	if args[0] == "init-admin" {
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("初始化参数无效")
		}
	}
	app, err := player.Open(dataDir, player.Options{Media: nas.New(), SecureCookies: os.Getenv("PLAYER_SECURE_COOKIES") == "1"})
	if err != nil {
		return err
	}
	defer app.Close()
	if args[0] == "init-admin" {
		if ifNeeded {
			found, err := app.HasAdmin()
			if err != nil || found {
				return err
			}
		}
		return initAdmin(app, user)
	}
	return serve(app, address)
}

func main() {
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Print(err)
		os.Exit(1)
	}
}
