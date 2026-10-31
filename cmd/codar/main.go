package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/codar-gateway/codar-gateway/internal/gateway"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func run() error {
	var cfg gateway.Config
	var version bool
	var oldDir, hubProxies string
	flag.StringVar(&cfg.Addr, "addr", env("CODAR_ADDR", "127.0.0.1:8801"), "HTTP listen address")
	flag.StringVar(&cfg.DataDir, "data-dir", env("CODAR_DATA_DIR", "data"), "persistent data directory")
	flag.StringVar(&oldDir, "import-python", "", "copy a Python Codar data directory into a new empty data directory and exit")
	flag.StringVar(&hubProxies, "import-hub-proxies", "", "import Codar bindings from a Hub proxies.json with --import-python")
	flag.BoolVar(&version, "version", false, "print version and exit")
	flag.Parse()
	if version {
		fmt.Println("Codar2API " + gateway.Version)
		return nil
	}
	if oldDir != "" {
		return gateway.ImportPython(oldDir, cfg.DataDir, hubProxies)
	}
	if hubProxies != "" {
		return errors.New("--import-hub-proxies must be used with --import-python")
	}
	cfg.Password = os.Getenv("CODAR_ADMIN_PASSWORD")
	// Windows users usually double-click the exe and miss the one-time random
	// password in the console, so first start there defaults to "admin".
	if runtime.GOOS == "windows" {
		cfg.DefaultPassword = "admin"
	}
	cfg.APIBase = os.Getenv("CODAR_API_BASE")
	cfg.PayBase = os.Getenv("CODAR_PAY_BASE")
	cfg.WebBase = os.Getenv("CODAR_WEB_BASE")
	cfg.CheckURL = os.Getenv("CODAR_PROXY_CHECK_URL")
	cfg.LocalConfig = os.Getenv("CODAR_CONFIG")
	cfg.SecureCookie = os.Getenv("CODAR_SECURE_COOKIE") == "true"
	cfg.MaxConcurrent, _ = strconv.Atoi(os.Getenv("CODAR_MAX_CONCURRENT"))
	app, initial, e := gateway.New(cfg)
	if e != nil {
		return e
	}
	defer func() {
		if e := app.Close(); e != nil {
			log.Printf("shutdown flush: %v", e)
		}
	}()
	if initial != "" && initial == cfg.DefaultPassword {
		log.Printf("首次启动，管理员默认密码为 %s，请登录后在「设置」中修改", initial)
	} else if initial != "" {
		log.Printf("首次启动管理员密码（仅显示一次）: %s", initial)
	}
	server := &http.Server{Addr: cfg.Addr, Handler: app, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 64 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() {
		log.Printf("Codar2API %s listening on http://%s", gateway.Version, cfg.Addr)
		done <- server.ListenAndServe()
	}()
	select {
	case e = <-done:
		if !errors.Is(e, http.ErrServerClosed) {
			return e
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if e = server.Shutdown(shutdown); e != nil {
			_ = server.Close()
		}
		<-done
	}
	return nil
}
func main() {
	if e := run(); e != nil {
		log.Print(e)
		os.Exit(1)
	}
}
