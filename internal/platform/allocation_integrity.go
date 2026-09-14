package platform

import (
	"context"
	"time"
)

// File integrity is a platform maintenance concern, independent of allocation execution.
func (a *App) inspectAllocationSources(ctx context.Context) error {
	values, err := rows(ctx, a.Pool, `SELECT row_to_json(s) FROM platform_resume_sources s WHERE verified AND EXISTS(SELECT 1 FROM platform_pool_memberships m WHERE m.resume_id=s.resume_id AND m.status='pending_allocation') ORDER BY last_checked_at NULLS FIRST,resume_id LIMIT 20`)
	if err != nil {
		return err
	}
	for _, source := range values {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		path, e := a.resumeFile(str(source["file_path"]))
		checksum := ""
		if e == nil {
			checksum, _, e = fileDigest(ctx, path)
		}
		valid := e == nil && checksum == source["file_checksum"]
		if _, err = a.Pool.Exec(ctx, `UPDATE platform_resume_sources SET verified=$3,revision=revision+CASE WHEN $3 THEN 0 ELSE 1 END,last_checked_at=now() WHERE resume_id=$1 AND revision=$2`, source["resume_id"], source["revision"], valid); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) allocationIntegrityWorker(ctx context.Context) {
	for pause(ctx, time.Minute) {
		inspect, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := a.inspectAllocationSources(inspect)
		cancel()
		if err != nil && ctx.Err() == nil {
			a.Log.Warn("allocation source integrity scan incomplete", "code", "integrity_scan_incomplete")
		}
	}
}
