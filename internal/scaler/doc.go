// Package scaler is the auto-scale logic: reads Temporal task-queue
// depth + active-goal count and scales worker + twitter replicas
// between MIN_INSTANCES and MAX_INSTANCES via the Docker API.
package scaler
