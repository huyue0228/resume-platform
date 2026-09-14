package platform

import (
	"context"
	"fmt"
)

func (a *App) migrateAllocation(ctx context.Context, db DB) error {
	var conflicts int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM (SELECT workflow_id FROM core_assignmentattempt WHERE status IN ('pending_review','pending_dispatch','dispatched','passed') GROUP BY workflow_id HAVING count(*)>1) c`).Scan(&conflicts); err != nil {
		return err
	}
	if conflicts > 0 {
		return fmt.Errorf("allocation migration: %d workflows have conflicting active attempts; resolve explicitly before upgrading", conflicts)
	}
	_, err := db.Exec(ctx, `
DO $$ BEGIN IF EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='core_resume' AND column_name='resume_file' AND character_maximum_length<1024) THEN DROP TRIGGER IF EXISTS allocation_source_revision ON core_resume; ALTER TABLE core_resume ALTER COLUMN resume_file TYPE varchar(1024); END IF; END $$;
CREATE TABLE IF NOT EXISTS platform_resume_sources(resume_id bigint PRIMARY KEY REFERENCES core_resume(id) ON DELETE CASCADE,revision bigint NOT NULL DEFAULT 1,file_path text NOT NULL DEFAULT '',file_checksum text NOT NULL DEFAULT '',verified boolean NOT NULL DEFAULT false);
ALTER TABLE platform_resume_sources ADD COLUMN IF NOT EXISTS last_checked_at timestamptz;
DO $$ BEGIN IF EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='platform_resume_sources'::regclass AND conname='platform_resume_sources_resume_id_fkey' AND confdeltype<>'c') THEN ALTER TABLE platform_resume_sources DROP CONSTRAINT platform_resume_sources_resume_id_fkey; ALTER TABLE platform_resume_sources ADD CONSTRAINT platform_resume_sources_resume_id_fkey FOREIGN KEY(resume_id) REFERENCES core_resume(id) ON DELETE CASCADE; END IF; END $$;
INSERT INTO platform_resume_sources(resume_id,file_path) SELECT id,COALESCE(resume_file,'') FROM core_resume ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS platform_allocation_scopes(id bigserial PRIMARY KEY,entity text NOT NULL,pool_code text NOT NULL,revision bigint NOT NULL DEFAULT 1,epoch bigint NOT NULL DEFAULT 1,next_sequence bigint NOT NULL DEFAULT 1,allocation_mode text NOT NULL DEFAULT 'legacy' CHECK(allocation_mode IN ('legacy','simulate','execute_v1')),lease_token text NOT NULL DEFAULT '',lease_until timestamptz,UNIQUE(entity,pool_code));
ALTER TABLE platform_allocation_scopes ADD COLUMN IF NOT EXISTS paused boolean NOT NULL DEFAULT false;
CREATE TABLE IF NOT EXISTS platform_allocation_changes(id bigserial PRIMARY KEY,scope_id bigint NOT NULL REFERENCES platform_allocation_scopes(id),created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS platform_allocation_changes_scope ON platform_allocation_changes(scope_id,id);
CREATE TABLE IF NOT EXISTS platform_demand_settings(demand_id bigint PRIMARY KEY REFERENCES core_job(id),reception_state text NOT NULL CHECK(reception_state IN ('receiving','paused','closed')),revision bigint NOT NULL DEFAULT 1,updated_by bigint REFERENCES accounts_user(id),updated_at timestamptz NOT NULL DEFAULT now(),reason text NOT NULL DEFAULT 'migration');
CREATE TABLE IF NOT EXISTS platform_screening_qualifications(id bigserial PRIMARY KEY,member_id bigint NOT NULL REFERENCES platform_pool_memberships(id),revision bigint NOT NULL,decision_id bigint NOT NULL REFERENCES core_agentdispatchdecision(id),source_revision bigint NOT NULL,source_checksum text NOT NULL,standard_hash text NOT NULL,tags_hash text NOT NULL,tags jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(member_id,revision));
CREATE TABLE IF NOT EXISTS platform_allocation_tasks(id bigserial PRIMARY KEY,scope_id bigint NOT NULL REFERENCES platform_allocation_scopes(id),mode text NOT NULL CHECK(mode IN ('simulate','execute')),status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','running','completed','failed','cancelled')),generation bigint NOT NULL DEFAULT 1,idempotency_key text UNIQUE NOT NULL,input_hash text NOT NULL DEFAULT '',member_ids jsonb NOT NULL DEFAULT '[]',snapshot jsonb,pin jsonb,worker_token text NOT NULL DEFAULT '',lease_until timestamptz,error_code text NOT NULL DEFAULT '',progress jsonb NOT NULL DEFAULT '{}',created_by bigint REFERENCES accounts_user(id),created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now());
ALTER TABLE platform_allocation_tasks ADD COLUMN IF NOT EXISTS kernel_namespace text NOT NULL DEFAULT gen_random_uuid()::text;
ALTER TABLE platform_allocation_tasks ADD COLUMN IF NOT EXISTS automatic boolean NOT NULL DEFAULT false;
ALTER TABLE platform_allocation_tasks ADD COLUMN IF NOT EXISTS execution_version text NOT NULL DEFAULT 'v1';
CREATE TABLE IF NOT EXISTS platform_allocation_work_items(id bigserial PRIMARY KEY,member_id bigint NOT NULL REFERENCES platform_pool_memberships(id),qualification_id bigint NOT NULL REFERENCES platform_screening_qualifications(id),task_id bigint REFERENCES platform_allocation_tasks(id),status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','leased','assigned','waiting','failed','cancelled')),reason_code text NOT NULL DEFAULT '',observed_revision bigint NOT NULL DEFAULT 0,created_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX IF NOT EXISTS platform_allocation_active_work ON platform_allocation_work_items(qualification_id) WHERE status IN ('pending','leased');
CREATE INDEX IF NOT EXISTS platform_allocation_pending_work ON platform_allocation_work_items(status,member_id);
CREATE TABLE IF NOT EXISTS platform_allocation_plans(id bigserial PRIMARY KEY,task_id bigint NOT NULL REFERENCES platform_allocation_tasks(id),generation bigint NOT NULL,snapshot_hash text NOT NULL,result jsonb NOT NULL,status text NOT NULL CHECK(status IN ('proposed','simulated','committed','stale','invalid','cancelled')),validation_code text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(task_id,generation));
CREATE TABLE IF NOT EXISTS platform_assignment_targets(attempt_id bigint PRIMARY KEY REFERENCES core_assignmentattempt(id),member_id bigint REFERENCES platform_pool_memberships(id),qualification_id bigint REFERENCES platform_screening_qualifications(id),plan_id bigint REFERENCES platform_allocation_plans(id),scope_id bigint REFERENCES platform_allocation_scopes(id),demand_id bigint REFERENCES core_job(id),department_id bigint REFERENCES core_department(id),target_kind text NOT NULL CHECK(target_kind IN ('demand','department_only','unknown')),demand_name text NOT NULL DEFAULT '',department_name text NOT NULL DEFAULT '',sequence bigint NOT NULL DEFAULT 0,allocated_at timestamptz NOT NULL DEFAULT now(),dispatched_at timestamptz);
CREATE INDEX IF NOT EXISTS platform_assignment_supply ON platform_assignment_targets(scope_id,allocated_at,demand_id);

INSERT INTO platform_assignment_targets(attempt_id,department_id,target_kind,department_name,allocated_at,dispatched_at)
SELECT id,initial_department_id,'department_only',initial_department_name_snapshot,created_at,dispatched_at FROM core_assignmentattempt WHERE source='manual' ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS platform_allocation_audit(id bigserial PRIMARY KEY,scope_id bigint REFERENCES platform_allocation_scopes(id),demand_id bigint REFERENCES core_job(id),actor_id bigint REFERENCES accounts_user(id),kind text NOT NULL,payload jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now());
CREATE OR REPLACE FUNCTION platform_allocation_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'screening qualifications are immutable'; END $$;
DROP TRIGGER IF EXISTS allocation_qualification_immutable ON platform_screening_qualifications;
CREATE TRIGGER allocation_qualification_immutable BEFORE UPDATE OR DELETE ON platform_screening_qualifications FOR EACH ROW EXECUTE FUNCTION platform_allocation_immutable();
CREATE OR REPLACE FUNCTION platform_source_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  INSERT INTO platform_resume_sources(resume_id,file_path) VALUES(NEW.id,COALESCE(NEW.resume_file,'')) ON CONFLICT DO NOTHING;
 ELSIF NEW.resume_file IS DISTINCT FROM OLD.resume_file THEN
  INSERT INTO platform_resume_sources(resume_id,file_path) VALUES(NEW.id,COALESCE(NEW.resume_file,'')) ON CONFLICT(resume_id) DO UPDATE SET revision=platform_resume_sources.revision+1,file_path=EXCLUDED.file_path,file_checksum='',verified=false;
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS allocation_source_revision ON core_resume;
CREATE TRIGGER allocation_source_revision AFTER INSERT OR UPDATE OF resume_file ON core_resume FOR EACH ROW EXECUTE FUNCTION platform_source_revision();
CREATE OR REPLACE FUNCTION platform_allocation_changed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE rowdata jsonb; candidate bigint; workflow bigint; resume bigint;
BEGIN
 rowdata=CASE WHEN TG_OP='DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END;
 IF TG_OP='UPDATE' AND to_jsonb(OLD)=to_jsonb(NEW) THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME='platform_resume_sources' AND TG_OP='UPDATE' AND (to_jsonb(OLD)-'last_checked_at')=(to_jsonb(NEW)-'last_checked_at') THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME='core_candidateworkflow' AND TG_OP='UPDATE' AND (to_jsonb(OLD)-'updated_at'-'active_processing_expires_at'-'active_processing_token'-'active_processing_scope_item_id')=(to_jsonb(NEW)-'updated_at'-'active_processing_expires_at'-'active_processing_token'-'active_processing_scope_item_id') THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME='core_candidate' THEN candidate=(rowdata->>'id')::bigint;
 ELSIF TG_TABLE_NAME='core_candidateworkflow' THEN workflow=(rowdata->>'id')::bigint;
 ELSIF TG_TABLE_NAME='core_assignmentattempt' THEN workflow=(rowdata->>'workflow_id')::bigint;
 ELSIF TG_TABLE_NAME='platform_pool_memberships' THEN candidate=(rowdata->>'candidate_id')::bigint;
 ELSIF TG_TABLE_NAME='core_resume' THEN resume=(rowdata->>'id')::bigint;
 ELSIF TG_TABLE_NAME='platform_resume_sources' THEN resume=(rowdata->>'resume_id')::bigint;
 END IF;
 -- Append-only invalidations avoid acquiring a scope lock after a business row lock.
 INSERT INTO platform_allocation_changes(scope_id)
 SELECT s.id FROM platform_allocation_scopes s WHERE (candidate IS NULL AND workflow IS NULL AND resume IS NULL) OR EXISTS(SELECT 1 FROM platform_pool_memberships m WHERE m.pool_code=s.pool_code AND m.assessment->'pool'->>'entity'=s.entity AND ((candidate IS NOT NULL AND m.candidate_id=candidate) OR (workflow IS NOT NULL AND m.workflow_id=workflow) OR (resume IS NOT NULL AND m.resume_id=resume)));
 IF TG_TABLE_NAME='core_assignmentattempt' AND TG_OP<>'DELETE' AND rowdata->>'status' IN ('dispatched','passed','rejected') THEN
  UPDATE platform_assignment_targets SET dispatched_at=COALESCE(dispatched_at,now()) WHERE attempt_id=(rowdata->>'id')::bigint;
 END IF;
 RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;
`)
	if err != nil {
		return err
	}
	for _, table := range []string{"core_candidate", "core_candidateworkflow", "core_resume", "core_assignmentattempt", "platform_pool_memberships", "platform_resume_sources", "core_job", "core_department", "platform_pool_policy", "platform_demand_settings"} {
		if _, err = db.Exec(ctx, "DROP TRIGGER IF EXISTS allocation_changed ON "+quote(table)+"; CREATE TRIGGER allocation_changed AFTER INSERT OR UPDATE OR DELETE ON "+quote(table)+" FOR EACH ROW EXECUTE FUNCTION platform_allocation_changed()"); err != nil {
			return err
		}
	}
	return a.syncAllocationConfig(ctx, db)
}

func (a *App) syncAllocationConfig(ctx context.Context, db DB) error {
	_, err := db.Exec(ctx, `
INSERT INTO platform_allocation_scopes(entity,pool_code) SELECT p->>'entity',p->>'code' FROM platform_pool_policy, jsonb_array_elements(policy->'pools') p ON CONFLICT DO NOTHING;
INSERT INTO platform_demand_settings(demand_id,reception_state)
SELECT j.id,CASE WHEN j.is_active AND EXISTS(SELECT 1 FROM platform_pool_policy,jsonb_array_elements(policy->'rules') r WHERE (r->>'job_id')::bigint=j.id AND COALESCE((r->>'active')::boolean,true)) THEN 'receiving' ELSE 'paused' END FROM core_job j ON CONFLICT DO NOTHING;
`)
	return err
}

func ensureAllocationAttemptIndex(ctx context.Context, db DB) error {
	var indexed bool
	if err := db.QueryRow(ctx, `SELECT to_regclass('platform_one_effective_attempt') IS NOT NULL`).Scan(&indexed); err != nil {
		return err
	}
	if !indexed {
		if _, err := db.Exec(ctx, `CREATE UNIQUE INDEX platform_one_effective_attempt ON core_assignmentattempt(workflow_id) WHERE status IN ('pending_review','pending_dispatch','dispatched','passed')`); err != nil {
			return err
		}
	}

	return nil
}
