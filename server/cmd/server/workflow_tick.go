package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
)

// Workflow reconcile tick (feat/workflow-v0). The engine is deliberately
// poll-driven: it never hooks issue-status write paths, so a lost event can
// at worst delay a transition by one tick, never wedge a run. 30s matches the
// granularity of the work being supervised (agent tasks are minutes-long).
const defaultWorkflowTickInterval = 30 * time.Second

func workflowTickInterval() time.Duration {
	if v := os.Getenv("WORKFLOW_TICK_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= time.Second {
			return d
		}
	}
	return defaultWorkflowTickInterval
}

func runWorkflowTick(ctx context.Context, svc *service.WorkflowService) {
	interval := workflowTickInterval()
	slog.Info("workflow tick started", "interval", interval.String())
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("workflow tick stopped")
			return
		case <-t.C:
			svc.ReconcileAll(ctx)
		}
	}
}
