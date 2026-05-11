package scaletozero

import (
	"sync"
	"syscall"

	"github.com/umuttalha/umut/internal/state"
)

type pidTracker struct {
	pids sync.Map
}

func newPIDTracker() *pidTracker {
	return &pidTracker{}
}

func (t *pidTracker) set(projectName, serviceName string, pid int) {
	t.pids.Store(key(projectName, serviceName), pid)
}

func (t *pidTracker) get(projectName, serviceName string) int {
	if val, ok := t.pids.Load(key(projectName, serviceName)); ok {
		return val.(int)
	}
	return 0
}

func (t *pidTracker) delete(projectName, serviceName string) {
	t.pids.Delete(key(projectName, serviceName))
}

func (t *pidTracker) isRunning(projectName, serviceName string) bool {
	return t.get(projectName, serviceName) > 0
}

func (t *pidTracker) populateFromStore(store *state.Store) {
	for _, project := range store.List() {
		for _, svc := range project.Services {
			if svc.PID > 0 && isProcessRunning(svc.PID) {
				t.set(project.Name, svc.Name, svc.PID)
			}
		}
	}
}

func (t *pidTracker) reconcileFromStore(store *state.Store) {
	for _, project := range store.List() {
		for _, svc := range project.Services {
			k := key(project.Name, svc.Name)
			existing := t.get(project.Name, svc.Name)
			if svc.PID > 0 && svc.PID != existing && isProcessRunning(svc.PID) {
				t.pids.Store(k, svc.PID)
			}
		}
	}
}

func key(projectName, serviceName string) string {
	return projectName + "/" + serviceName
}

func isProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
