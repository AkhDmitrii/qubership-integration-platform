package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/agentsignature"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/doccycle"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/docgithub"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/experimentruntime"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/localwork"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/openrouterusage"
	"github.com/Netcracker/qubership-integration-platform/tools/autonomous-fixer/internal/workcycle"
)

func validateHostedRuntime(ctx context.Context, cfg localwork.Config) error {
	if !filepath.IsAbs(cfg.RuntimeBinary) || cfg.RuntimeVersion == "" {
		return fmt.Errorf("hosted work requires an absolute OpenCode runtime path and runtime-version")
	}
	if strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) == "" {
		return fmt.Errorf("hosted work requires OPENROUTER_API_KEY")
	}
	for _, profile := range []experimentruntime.Profile{cfg.Profile, *cfg.ReviewProfile} {
		if err := experimentruntime.ValidateHostedOpenCodeProfile(profile); err != nil {
			return err
		}
	}
	command := exec.CommandContext(ctx, cfg.RuntimeBinary, "--version")
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8"}
	version, err := command.Output()
	if err != nil {
		return fmt.Errorf("read OpenCode version: %w", err)
	}
	if strings.TrimSpace(string(version)) != cfg.RuntimeVersion {
		return fmt.Errorf("OpenCode version differs from runtime-version")
	}
	return nil
}

func exportHandoff(ctx context.Context, plan docgithub.Plan, output, workspace, destination string) error {
	output, err := filepath.EvalSymlinks(output)
	if err != nil {
		return err
	}
	destination = filepath.Clean(destination)
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return fmt.Errorf("handoff destination must be a new directory")
	}
	tmp, err := os.MkdirTemp(filepath.Dir(destination), ".docs-handoff-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	var secrets [][]byte
	for _, key := range []string{"OPENROUTER_API_KEY", "GITHUB_TOKEN", "GH_TOKEN"} {
		value := strings.TrimSpace(os.Getenv(key))
		if value != "" {
			encoded, _ := json.Marshal(value)
			secrets = append(secrets, []byte(value), encoded[1:len(encoded)-1])
		}
	}
	copyFile := func(rel string, optional bool) error {
		source := filepath.Join(output, rel)
		if err := regularPath(output, source, false); err != nil {
			if optional && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		info, err := os.Lstat(source)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("handoff source must be a regular file")
		}
		content, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		for _, secret := range secrets {
			if bytes.Contains(content, secret) || bytes.Contains([]byte(rel), secret) {
				return fmt.Errorf("handoff contains a controller credential")
			}
		}
		target := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		return os.WriteFile(target, content, 0600)
	}
	for _, name := range []string{
		"feedback.json", "task/task.md", "task/plan.json", "task/packet.json", "task/jev-report.json",
		"work/config.json", "work/context.json", "work/report.json", "work/receipt.json",
	} {
		if err := copyFile(name, false); err != nil {
			return err
		}
	}
	runDir := filepath.Join(output, "work")
	targetRun := filepath.Join(tmp, "work")
	var cfg localwork.Config
	if err := read(filepath.Join(targetRun, "config.json"), &cfg); err != nil {
		return err
	}
	if !cfg.Hosted || cfg.ResumeFrom != "" || cfg.OutputDirectory != runDir || cfg.TaskDirectory != filepath.Join(output, "task") {
		return fmt.Errorf("handoff requires a fresh hosted documentation run")
	}
	var receipt struct {
		Evidence map[string]string `json:"evidence_sha256"`
		Prior    map[string]string `json:"prior_evidence_sha256"`
	}
	if err := read(filepath.Join(targetRun, "receipt.json"), &receipt); err != nil {
		return err
	}
	if len(receipt.Prior) != 0 {
		return fmt.Errorf("handoff requires evidence from the current run")
	}
	for name := range receipt.Evidence {
		rel, err := filepath.Rel(runDir, name)
		if err != nil || !strings.HasPrefix(rel, "evidence"+string(filepath.Separator)) {
			return fmt.Errorf("handoff receipt refers to a path outside evidence")
		}
		if err := copyFile(filepath.Join("work", rel), false); err != nil {
			return err
		}
	}
	var report workcycle.Report
	if err := read(filepath.Join(targetRun, "report.json"), &report); err != nil {
		return err
	}
	for _, name := range report.Candidate.ChangedPaths {
		if !strings.HasPrefix(name, "help/docs/") || filepath.Clean(name) != name || strings.Contains(name, "\\") {
			return fmt.Errorf("handoff candidate modifies a path outside help")
		}
		if err := copyFile(filepath.Join("work", "candidate-files", name), true); err != nil {
			return err
		}
	}
	for i, observation := range report.Observations {
		switch observation.Stage {
		case doccycle.AssessStage, doccycle.WriteStage, doccycle.ReviewStage, doccycle.VerifyStage:
		default:
			return fmt.Errorf("handoff report contains an unknown documentation stage")
		}
		dir := filepath.Join("work", fmt.Sprintf("step-%04d-%s", i+1, observation.Stage))
		for _, name := range []string{"profile.json", agentsignature.IdentityFilename} {
			if err := copyFile(filepath.Join(dir, name), false); err != nil {
				return err
			}
		}
	}
	if len(report.Observations) != 0 {
		if err := copyFile(filepath.Join("work", openrouterusage.Filename), false); err != nil {
			return err
		}
	}
	if _, err := docgithub.ValidateCandidate(ctx, plan, targetRun, workspace); err != nil {
		return err
	}
	return os.Rename(tmp, destination)
}
