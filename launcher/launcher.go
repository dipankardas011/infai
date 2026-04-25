package launcher

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/dipankardas011/infai/model"
)

func BuildArgs(serverBin string, m model.ModelEntry, p model.Profile) []string {
	args := []string{serverBin, "-m", m.GGUFPath}

	if p.UseMmproj && m.MmprojPath != "" {
		args = append(args, "--mmproj", m.MmprojPath)
	}

	args = append(args,
		"--port", strconv.Itoa(p.Port),
		"--host", p.Host,
		"-c", strconv.Itoa(p.ContextSize),
		"-ngl", p.NGL,
	)

	if p.BatchSize != nil {
		args = append(args, "-b", strconv.Itoa(*p.BatchSize))
	}
	if p.UBatchSize != nil {
		args = append(args, "-ub", strconv.Itoa(*p.UBatchSize))
	}
	if p.CacheTypeK != nil {
		args = append(args, "--cache-type-k", *p.CacheTypeK)
	}
	if p.CacheTypeV != nil {
		args = append(args, "--cache-type-v", *p.CacheTypeV)
	}
	if p.FlashAttn {
		args = append(args, "--flash-attn", "on")
	}
	if p.Jinja {
		args = append(args, "--jinja")
	}
	if p.Temperature != nil {
		args = append(args, "--temperature", strconv.FormatFloat(*p.Temperature, 'f', -1, 64))
	}
	if p.ReasoningBudget != nil {
		args = append(args, "--reasoning-budget", strconv.Itoa(*p.ReasoningBudget))
	}
	if p.TopP != nil {
		args = append(args, "--top_p", strconv.FormatFloat(*p.TopP, 'f', -1, 64))
	}
	if p.TopK != nil {
		args = append(args, "--top_k", strconv.Itoa(*p.TopK))
	}
	if p.NoKVOffload {
		args = append(args, "--no-kv-offload")
	}
	if p.ExtraFlags != "" {
		args = append(args, strings.Fields(p.ExtraFlags)...)
	}

	return args
}

func BuildCommand(serverBin string, m model.ModelEntry, p model.Profile) string {
	args := BuildArgs(serverBin, m, p)
	quoted := make([]string, len(args))
	for i, a := range args {
		if strings.ContainsAny(a, " \t") {
			quoted[i] = fmt.Sprintf("%q", a)
		} else {
			quoted[i] = a
		}
	}
	return strings.Join(quoted, " ")
}

func Exec(args []string) error {
	return syscall.Exec(args[0], args, os.Environ())
}
