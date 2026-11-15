package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"artex/config"
	"artex/defense"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch args[0] {
	case "audit":
		err = runAudit(ctx, args[1:])
	case "run":
		err = runDefense(ctx, args[1:])
	case "review":
		err = runReview(args[1:])
	case "check-task":
		err = runCheck(args[1:])
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "artex-defense:", err)
		return 1
	}
	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, `ARTEX Defense — local, evidence-backed remediation

Usage:
  artex-defense check-task -task task.json
  artex-defense audit -task task.json [-out findings.json]
  artex-defense run -task task.json -out review-bundle/
  artex-defense review -bundle review-bundle/bundle.json -reviewer "name"

The run command never edits the source repository, commits, pushes, merges, or deploys.
Validation requires a locally available container image. Network access is disabled by default.`)
}

func loadTask(path, repo string) (defense.DefenseTask, error) {
	var task defense.DefenseTask
	if path != "" {
		file, err := os.Open(path)
		if err != nil {
			return task, err
		}
		decoder := json.NewDecoder(file)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&task); err != nil {
			file.Close()
			return task, fmt.Errorf("parse task: %w", err)
		}
		if err := file.Close(); err != nil {
			return task, err
		}
	} else if configured := config.Load().Defense; configured != nil {
		task = *configured
	} else {
		task = defense.DefaultTask(repo)
	}
	if repo != "" {
		task.RepositoryRoot = repo
	}
	if err := defense.ValidateTask(&task); err != nil {
		return task, err
	}
	return task, nil
}

func taskFlags(fs *flag.FlagSet) (*string, *string) {
	return fs.String("task", "", "versioned DefenseTask JSON"), fs.String("repo", "", "repository root override")
}

func runCheck(args []string) error {
	fs := flag.NewFlagSet("check-task", flag.ContinueOnError)
	taskPath, repo := taskFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	task, err := loadTask(*taskPath, *repo)
	if err != nil {
		return err
	}
	return writeJSON(os.Stdout, task)
}

func runAudit(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	taskPath, repo := taskFlags(fs)
	out := fs.String("out", "", "write assessment JSON outside the repository")
	if err := fs.Parse(args); err != nil {
		return err
	}
	task, err := loadTask(*taskPath, *repo)
	if err != nil {
		return err
	}
	coordinator, err := defense.NewCoordinator(task)
	if err != nil {
		return err
	}
	assessment, err := coordinator.Audit(ctx)
	if err != nil {
		return err
	}
	if *out != "" {
		return writeFileJSON(*out, assessment)
	}
	return writeJSON(os.Stdout, assessment)
}

func runDefense(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	taskPath, repo := taskFlags(fs)
	out := fs.String("out", "", "review bundle directory outside the repository")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("-out is required")
	}
	task, err := loadTask(*taskPath, *repo)
	if err != nil {
		return err
	}
	coordinator, err := defense.NewCoordinator(task)
	if err != nil {
		return err
	}
	bundle, runErr := coordinator.Run(ctx)
	exportErr := defense.ExportBundle(bundle, *out)
	if runErr != nil {
		return errors.Join(runErr, exportErr)
	}
	if exportErr != nil {
		return exportErr
	}
	fmt.Printf("task %s: %s; review bundle: %s\n", bundle.Task.ID, bundle.Task.Status, *out)
	return nil
}

func runReview(args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	path := fs.String("bundle", "", "bundle.json path")
	reviewer := fs.String("reviewer", "", "independent human reviewer identity")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" || *reviewer == "" {
		return errors.New("-bundle and -reviewer are required")
	}
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	var bundle defense.Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		return err
	}
	if err := defense.ReviewBundle(&bundle, *reviewer); err != nil {
		return err
	}
	return writeFileJSON(*path, bundle)
}

func writeFileJSON(path string, value any) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := writeJSON(file, value); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func writeJSON(file *os.File, value any) error {
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
