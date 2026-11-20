package main

import (
	"flag"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "cpu" {
		runCPU(os.Args[2:])
		return
	}
	listen := flag.String("listen", "127.0.0.1:18080", "local address that receives redirected traffic")
	upstream := flag.String("upstream", "127.0.0.1:8080", "real service address")
	delay := flag.Duration("delay", 800*time.Millisecond, "how long to hold the response")
	loss := flag.Float64("loss", 0, "percent of connections to drop")
	flag.Parse()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("listening on %s, delaying responses by %s, dropping %.0f%%", *listen, *delay, *loss)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		if *loss > 0 && rand.Float64()*100 < *loss {
			conn.Close()
			continue
		}
		go proxy(conn, *upstream, *delay)
	}
}

func runCPU(args []string) {
	fs := flag.NewFlagSet("cpu", flag.ExitOnError)
	percent := fs.Float64("percent", 90, "share of one CPU to burn")
	_ = fs.Parse(args)
	_ = os.WriteFile("/tmp/crashlab-cpu.pid", []byte(strconv.Itoa(os.Getpid())), 0o644)
	runtime.GOMAXPROCS(1)
	if *percent < 1 {
		*percent = 1
	}
	if *percent > 100 {
		*percent = 100
	}
	on := time.Duration(*percent*100) * time.Microsecond
	off := time.Duration((100-*percent)*100) * time.Microsecond
	log.Printf("burning %.0f%% cpu", *percent)
	for {
		start := time.Now()
		for time.Since(start) < on {
		}
		if off > 0 {
			time.Sleep(off)
		}
	}
}

func proxy(client net.Conn, upstream string, delay time.Duration) {
	defer client.Close()
	up, err := net.DialTimeout("tcp", upstream, 2*time.Second)
	if err != nil {
		log.Printf("dial upstream: %v", err)
		return
	}
	defer up.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(up, client)
		if tcp, ok := up.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		_, _ = io.Copy(client, up)
	}()
	wg.Wait()
}
