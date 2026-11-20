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

	"path/filepath"
	"strings"

	"github.com/dkolancs/crashlab/internal/api"
	"github.com/dkolancs/crashlab/internal/discover"
	"github.com/dkolancs/crashlab/internal/dockerx"
	"github.com/dkolancs/crashlab/internal/experiment"
	"github.com/dkolancs/crashlab/internal/fault"
	"github.com/dkolancs/crashlab/internal/model"
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
	case "inject":
		os.Exit(injectCmd(os.Args[2:]))
	case "clear":
		os.Exit(clearCmd(os.Args[2:]))
	case "test":
		os.Exit(testCmd(os.Args[2:]))
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
  crashlab experiment run experiments/payment-latency.yaml
  crashlab inject latency payment-service --delay 800ms
  crashlab inject loss payment-service --percent 20
  crashlab inject kill payment-service
  crashlab inject cpu checkout-service --percent 90
  crashlab clear payment-service
  crashlab test experiments/
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
	experiment.ReconcileOpen(ctx, db)
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
		fmt.Fprintf(os.Stderr, "usage: crashlab experiment run [file.yaml | flags]\n")
		return 2
	}
	rest := args[1:]
	spec := experiment.Spec{}
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		parsed, err := experiment.ParseFile(rest[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return 2
		}
		spec = parsed
		rest = rest[1:]
	}
	fs := flag.NewFlagSet("experiment run", flag.ContinueOnError)
	compose := fs.String("compose", "", "compose file")
	dbPath := fs.String("db", "crashlab.db", "sqlite database")
	netem := fs.String("netem", "", "netem helper directory")
	project := fs.String("project", "", "compose project name override")
	name := fs.String("name", "", "experiment name")
	target := fs.String("target", "", "service that receives the fault")
	faultType := fs.String("fault", "", "latency, loss, kill, or cpu")
	delay := fs.String("delay", "", "egress delay")
	percent := fs.Float64("percent", 0, "loss or cpu percent")
	baseline := fs.Duration("baseline", 0, "baseline window")
	duration := fs.Duration("duration", 0, "fault window")
	recovery := fs.Duration("recovery", 0, "recovery window")
	entry := fs.String("entry", "", "load generator URL")
	entryService := fs.String("entry-service", "", "service name for entry traffic")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *compose != "" {
		spec.ComposeFile = *compose
	}
	if *netem != "" {
		spec.NetemDir = *netem
	}
	if *project != "" {
		spec.Project = *project
	}
	if *name != "" {
		spec.Name = *name
	}
	if *target != "" {
		spec.Target = *target
	}
	if *faultType != "" {
		spec.FaultType = *faultType
	}
	if *delay != "" {
		parsed, err := time.ParseDuration(*delay)
		if err != nil {
			fmt.Fprintf(os.Stderr, "delay: %v\n", err)
			return 2
		}
		spec.Delay = parsed
	}
	if *percent > 0 {
		spec.LossPercent = *percent
		spec.CPUPercent = *percent
	}
	if *baseline > 0 {
		spec.Baseline = *baseline
	}
	if *duration > 0 {
		spec.Duration = *duration
	}
	if *recovery > 0 {
		spec.Recovery = *recovery
	}
	if *entry != "" {
		spec.EntryURL = *entry
	}
	if *entryService != "" {
		spec.EntryService = *entryService
	}
	return runSpec(*dbPath, spec)
}

func runSpec(dbPath string, spec experiment.Spec) int {
	spec = experiment.ApplyDefaults(spec)
	db, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	experiment.ReconcileOpen(ctx, db)
	runner := &experiment.Runner{Store: db}
	exp, err := runner.Run(ctx, spec)
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

func injectCmd(args []string) int {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: crashlab inject <latency|loss|kill|cpu> <service>\n")
		return 2
	}
	kind, service := args[0], args[1]
	fs := flag.NewFlagSet("inject", flag.ContinueOnError)
	compose := fs.String("compose", "demo/compose.yaml", "compose file")
	project := fs.String("project", "", "compose project")
	delay := fs.String("delay", "800ms", "latency")
	percent := fs.Float64("percent", 0, "loss or cpu percent")
	allowUntracked := fs.Bool("allow-untracked", false, "inject without an experiment record")
	if err := fs.Parse(args[2:]); err != nil {
		return 2
	}
	if !*allowUntracked {
		fmt.Fprintf(os.Stderr, "refusing to inject a fault outside an experiment; pass --allow-untracked or use crashlab experiment run\n")
		return 2
	}
	parsedDelay, err := time.ParseDuration(*delay)
	if err != nil {
		fmt.Fprintf(os.Stderr, "delay: %v\n", err)
		return 2
	}
	if *percent == 0 && kind == model.FaultLoss {
		*percent = 20
	}
	if *percent == 0 && kind == model.FaultCPU {
		*percent = 90
	}
	return withTarget(*compose, *project, service, func(ctx context.Context, id string) error {
		cli, err := dockerx.New()
		if err != nil {
			return err
		}
		defer cli.Close()
		if err := fault.Prepare(ctx, "netem"); err != nil {
			return err
		}
		return fault.Apply(ctx, cli, id, fault.Request{Kind: kind, Delay: parsedDelay, Percent: *percent})
	})
}

func clearCmd(args []string) int {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "usage: crashlab clear <service>\n")
		return 2
	}
	fs := flag.NewFlagSet("clear", flag.ContinueOnError)
	compose := fs.String("compose", "demo/compose.yaml", "compose file")
	project := fs.String("project", "", "compose project")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	return withTarget(*compose, *project, args[0], func(ctx context.Context, id string) error {
		cli, err := dockerx.New()
		if err != nil {
			return err
		}
		defer cli.Close()
		if err := fault.Prepare(ctx, "netem"); err != nil {
			return err
		}
		return fault.Clear(ctx, cli, id)
	})
}

func testCmd(args []string) int {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	dbPath := fs.String("db", "crashlab.db", "sqlite database")
	baseline := fs.String("baseline", "", "override baseline duration")
	duration := fs.String("duration", "", "override fault duration")
	recovery := fs.String("recovery", "", "override recovery duration")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "usage: crashlab test <file-or-directory>\n")
		return 2
	}
	files, err := experimentFiles(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	failed := false
	for _, file := range files {
		fmt.Printf("== %s\n", file)
		spec, err := experiment.ParseFile(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			failed = true
			continue
		}
		if err := applyDurationOverrides(&spec, *baseline, *duration, *recovery); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			failed = true
			continue
		}
		if runSpec(*dbPath, spec) != 0 {
			failed = true
		}
	}
	if failed {
		return 1
	}
	return 0
}

func applyDurationOverrides(spec *experiment.Spec, baseline, duration, recovery string) error {
	var err error
	if baseline != "" {
		if spec.Baseline, err = time.ParseDuration(baseline); err != nil {
			return fmt.Errorf("baseline: %w", err)
		}
	}
	if duration != "" {
		if spec.Duration, err = time.ParseDuration(duration); err != nil {
			return fmt.Errorf("duration: %w", err)
		}
	}
	if recovery != "" {
		if spec.Recovery, err = time.ParseDuration(recovery); err != nil {
			return fmt.Errorf("recovery: %w", err)
		}
	}
	return nil
}

func experimentFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			files = append(files, filepath.Join(path, name))
		}
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no experiment files in %s", path)
	}
	return files, nil
}

func withTarget(compose, project, service string, fn func(context.Context, string) error) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cli, err := dockerx.New()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	defer cli.Close()
	topo, err := discover.Inspect(ctx, cli, compose, project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	target, ok := topo.ByName(service)
	if !ok {
		fmt.Fprintf(os.Stderr, "service %s not found\n", service)
		return 1
	}
	if err := fn(ctx, target.ContainerID); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	fmt.Printf("%s %s\n", service, target.ContainerID[:12])
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
