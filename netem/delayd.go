package main

import (
	"flag"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18080", "local address that receives redirected traffic")
	upstream := flag.String("upstream", "127.0.0.1:8080", "real service address")
	delay := flag.Duration("delay", 800*time.Millisecond, "how long to hold the response")
	flag.Parse()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("listening on %s, delaying responses by %s", *listen, *delay)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go proxy(conn, *upstream, *delay)
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
