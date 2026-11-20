package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/dkolancs/crashlab/internal/api"
	"github.com/dkolancs/crashlab/internal/experiment"
	"github.com/dkolancs/crashlab/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("crashlab ")
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(runCmd(os.Args[2:]))
	case "experiment":
		os.Exit(experimentCmd(os.Args[2:]))
	case "report":
		os.Exit(reportCmd(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `CrashLab discovers a Docker Compose app, injects network latency, and reports how it propagates.

Usage:
  crashlab run [--compose demo/compose.yaml] [--addr :8080]
  crashlab experiment run --target payment-service --delay 800ms
  crashlab report <id>

`)
}

func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	compose := fs.String("compose", "demo/compose.yaml", "compose file")
	addr := fs.String("addr", ":8080", "API listen address")
	dbPath := fs.String("db", "crashlab.db", "sqlite database")
	netem := fs.String("netem", "netem", "netem helper directory")
	project := fs.String("project", "", "compose project name override")
	noWeb := fs.Bool("no-web", false, "do not start the Next.js dashboard")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !*noWeb {
		startWeb(ctx)
	}
	srv := &api.Server{
		Store:    db,
		Runner:   &experiment.Runner{Store: db},
		Compose:  *compose,
		Project:  *project,
		NetemDir: *netem,
		Base:     ctx,
	}
	log.Printf("API http://127.0.0.1%s", portOf(*addr))
	log.Printf("dashboard http://127.0.0.1:3000")
	if err := srv.ListenAndServe(ctx, *addr); err != nil {
		log.Fatal(err)
	}
	return 0
}

func experimentCmd(args []string) int {
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintf(os.Stderr, "usage: crashlab experiment run [flags]\n")
		return 2
	}
	fs := flag.NewFlagSet("experiment run", flag.ContinueOnError)
	compose := fs.String("compose", "demo/compose.yaml", "compose file")
	dbPath := fs.String("db", "crashlab.db", "sqlite database")
	netem := fs.String("netem", "netem", "netem helper directory")
	project := fs.String("project", "", "compose project name override")
	name := fs.String("name", "payment-latency", "experiment name")
	target := fs.String("target", "payment-service", "service that receives latency")
	delay := fs.String("delay", "800ms", "egress delay")
	baseline := fs.Duration("baseline", 15*time.Second, "baseline window")
	duration := fs.Duration("duration", 30*time.Second, "fault window")
	recovery := fs.Duration("recovery", 15*time.Second, "recovery window")
	entry := fs.String("entry", "http://127.0.0.1:8081/checkout", "load generator URL")
	entryService := fs.String("entry-service", "storefront", "service name for entry traffic")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	delayDur, err := time.ParseDuration(*delay)
	if err != nil {
		fmt.Fprintf(os.Stderr, "delay: %v\n", err)
		return 2
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner := &experiment.Runner{Store: db}
	exp, err := runner.Run(ctx, experiment.Spec{
		Name:         *name,
		Target:       *target,
		Delay:        delayDur,
		Baseline:     *baseline,
		Duration:     *duration,
		Recovery:     *recovery,
		EntryURL:     *entry,
		EntryService: *entryService,
		ComposeFile:  *compose,
		Project:      *project,
		NetemDir:     *netem,
	})
	text, _, ok, getErr := db.GetResult(exp.ID)
	if getErr == nil && ok {
		fmt.Print(text)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "experiment failed: %v\n", err)
		return 1
	}
	return 0
}

func reportCmd(args []string) int {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	dbPath := fs.String("db", "crashlab.db", "sqlite database")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "usage: crashlab report <id>\n")
		return 2
	}
	id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "id: %v\n", err)
		return 2
	}
	db, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	defer db.Close()
	text, _, ok, err := db.GetResult(id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "experiment %d has no report yet\n", id)
		return 1
	}
	fmt.Print(text)
	return 0
}

func startWeb(ctx context.Context) {
	if _, err := os.Stat("web/node_modules/next"); err != nil {
		log.Printf("dashboard dependencies missing; run npm install in web/")
		return
	}
	cmd := exec.CommandContext(ctx, "npm", "run", "dev", "--", "--port", "3000")
	cmd.Dir = "web"
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		log.Printf("dashboard: %v", err)
		return
	}
	go func() {
		<-ctx.Done()
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
	}()
	go func() { _ = cmd.Wait() }()
}

func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return ":" + port
}
