-- PostgreSQL业务关系设计参考；P00对齐宿主ID类型、迁移规范及索引后再实现。

-- 本文件不是生产迁移，不包含DROP，不宣称已在目标数据库执行。

-- 应用必须校验JSONB中的同租户/同项目ID、不可变版本和授权；本参考不替代这些规则。

CREATE TABLE bid_projects (
 tenant_id text NOT NULL,
 id text NOT NULL,
 owner_id text NOT NULL,
 agent_id text NOT NULL,
 title text,
 lifecycle text NOT NULL DEFAULT 'active' CHECK(lifecycle IN ('active','archived')),
 revision bigint NOT NULL DEFAULT 0,
 active_document_set_id text,
 latest_event_seq bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id, id)
);

CREATE TABLE bid_project_members (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 user_id text NOT NULL,
 role text NOT NULL CHECK(role IN ('author','reviewer','material_admin')),
 UNIQUE(tenant_id, project_id, user_id),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_session_links (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 session_id text NOT NULL,
 agent_id text NOT NULL,
 UNIQUE(tenant_id, session_id),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_source_files (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 role text NOT NULL,
 object_key text NOT NULL,
 sha256 char(64) NOT NULL,
 mime text NOT NULL,
 size_bytes bigint NOT NULL CHECK(size_bytes >= 0),
 file_version integer NOT NULL CHECK(file_version > 0),
 supersedes_file_id text,
 metadata jsonb NOT NULL DEFAULT '{}',
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_document_sets (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 file_versions jsonb NOT NULL,
 set_revision bigint NOT NULL,
 UNIQUE(tenant_id, project_id, set_revision),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_analysis_runs (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 document_set_id text NOT NULL,
 status text NOT NULL,
 parser_version text NOT NULL,
 prompt_version text NOT NULL,
 model_config_hash text NOT NULL,
 coverage jsonb NOT NULL DEFAULT '[]',
 FOREIGN KEY(tenant_id, project_id, document_set_id) REFERENCES bid_document_sets(tenant_id, project_id, id),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_analysis_corrections (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 analysis_id text NOT NULL,
 actor_id text NOT NULL,
 patches jsonb NOT NULL,
 input_revision bigint NOT NULL,
 reason text NOT NULL,
 FOREIGN KEY(tenant_id, project_id, analysis_id) REFERENCES bid_analysis_runs(tenant_id, project_id, id),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_source_blocks (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 analysis_id text NOT NULL,
 file_id text NOT NULL,
 ordinal bigint NOT NULL,
 block_kind text NOT NULL,
 content jsonb NOT NULL,
 locator jsonb NOT NULL,
 content_hash char(64) NOT NULL,
 FOREIGN KEY(tenant_id, project_id, analysis_id) REFERENCES bid_analysis_runs(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id, file_id) REFERENCES bid_source_files(tenant_id, project_id, id),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_lots (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 analysis_id text NOT NULL,
 lot_code text,
 name text NOT NULL,
 aliases jsonb NOT NULL DEFAULT '[]',
 sources jsonb NOT NULL,
 FOREIGN KEY(tenant_id, project_id, analysis_id) REFERENCES bid_analysis_runs(tenant_id, project_id, id),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_requirements (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 analysis_id text NOT NULL,
 kind text NOT NULL,
 verbatim text NOT NULL,
 normalized jsonb NOT NULL,
 scope_mode text NOT NULL CHECK(scope_mode IN ('all_lots','explicit','unresolved')),
 mandatory boolean,
 review_status text NOT NULL,
 sources jsonb NOT NULL,
 FOREIGN KEY(tenant_id, project_id, analysis_id) REFERENCES bid_analysis_runs(tenant_id, project_id, id),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_requirement_lots (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 requirement_id text NOT NULL,
 lot_id text NOT NULL,
 UNIQUE(tenant_id, project_id, requirement_id, lot_id),
 FOREIGN KEY(tenant_id, project_id, requirement_id) REFERENCES bid_requirements(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id, lot_id) REFERENCES bid_lots(tenant_id, project_id, id),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_selections (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 analysis_id text NOT NULL,
 selection_revision bigint NOT NULL,
 lot_ids jsonb NOT NULL,
 confirmed_by text NOT NULL,
 confirmed_at timestamptz NOT NULL,
 UNIQUE(tenant_id, project_id, selection_revision),
 FOREIGN KEY(tenant_id, project_id, analysis_id) REFERENCES bid_analysis_runs(tenant_id, project_id, id),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_confirmations (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 target_type text NOT NULL,
 target_id text NOT NULL,
 target_revision bigint NOT NULL,
 input_fingerprint char(64) NOT NULL,
 confirmed_by text NOT NULL,
 confirmed_at timestamptz NOT NULL,
 UNIQUE(tenant_id, project_id, target_type, target_id, target_revision, input_fingerprint),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_sections (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 lot_id text NOT NULL,
 parent_id text,
 title text NOT NULL,
 ordinal integer NOT NULL,
 strategy text NOT NULL CHECK(strategy IN ('template','narrative','data_table','attachment')),
 content_revision bigint NOT NULL DEFAULT 0,
 manual_locked boolean NOT NULL DEFAULT false,
 status text NOT NULL,
 FOREIGN KEY(tenant_id, project_id, lot_id) REFERENCES bid_lots(tenant_id, project_id, id),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_outline_versions (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 outline_revision bigint NOT NULL,
 tree_and_mappings jsonb NOT NULL,
 confirmation_id text,
 UNIQUE(tenant_id, project_id, outline_revision),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_section_versions (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 section_id text NOT NULL,
 content_revision bigint NOT NULL,
 author_type text NOT NULL,
 author_id text,
 input_fingerprint char(64) NOT NULL,
 ast jsonb NOT NULL,
 is_candidate boolean NOT NULL,
 UNIQUE(tenant_id, project_id, section_id, content_revision),
 FOREIGN KEY(tenant_id, project_id, section_id) REFERENCES bid_sections(tenant_id, project_id, id),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_requirement_sections (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 requirement_id text NOT NULL,
 section_id text NOT NULL,
 UNIQUE(tenant_id, project_id, requirement_id, section_id),
 FOREIGN KEY(tenant_id, project_id, requirement_id) REFERENCES bid_requirements(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id, section_id) REFERENCES bid_sections(tenant_id, project_id, id),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_fact_versions (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 fact_revision bigint NOT NULL,
 status text NOT NULL CHECK(status IN ('candidate','confirmed')),
 facts jsonb NOT NULL,
 confirmed_by text,
 confirmed_at timestamptz,
 input_fingerprint char(64) NOT NULL,
 UNIQUE(tenant_id, project_id, fact_revision),
 CHECK(status <> 'confirmed' OR (confirmed_by IS NOT NULL AND confirmed_at IS NOT NULL)),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_materials (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 knowledge_id text NOT NULL,
 source_revision text NOT NULL,
 file_sha256 char(64) NOT NULL,
 material_version bigint NOT NULL,
 material_type text NOT NULL,
 subject_id text,
 validity jsonb NOT NULL,
 page_groups jsonb NOT NULL,
 review_status text NOT NULL,
 source_snapshot jsonb NOT NULL,
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_material_versions (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 material_id text NOT NULL,
 material_version bigint NOT NULL,
 source_snapshot jsonb NOT NULL,
 page_groups jsonb NOT NULL,
 review_status text NOT NULL,
 UNIQUE(tenant_id, project_id, material_id, material_version),
 FOREIGN KEY(tenant_id, project_id, material_id) REFERENCES bid_materials(tenant_id, project_id, id),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_material_bindings (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 requirement_id text NOT NULL,
 section_id text,
 material_id text NOT NULL,
 material_version bigint NOT NULL,
 decision text NOT NULL,
 evidence_snapshot jsonb NOT NULL,
 FOREIGN KEY(tenant_id, project_id, requirement_id) REFERENCES bid_requirements(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id, material_id) REFERENCES bid_materials(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id, material_id, material_version) REFERENCES bid_material_versions(tenant_id, project_id, material_id, material_version),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_dependencies (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 from_type text NOT NULL,
 from_id text NOT NULL,
 from_version text NOT NULL,
 to_type text NOT NULL,
 to_id text NOT NULL,
 to_version text NOT NULL,
 stale_reason text,
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_review_issues (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 kind text NOT NULL,
 severity text NOT NULL,
 status text NOT NULL,
 requirement_id text,
 section_id text,
 locator jsonb,
 evidence jsonb NOT NULL,
 resolution jsonb,
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_jobs (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 kind text NOT NULL,
 status text NOT NULL,
 input_fingerprint char(64) NOT NULL,
 input_snapshot jsonb NOT NULL,
 attempt integer NOT NULL DEFAULT 0,
 lease_until timestamptz,
 heartbeat_at timestamptz,
 checkpoint jsonb NOT NULL DEFAULT '{}',
 result_ref text,
 error jsonb,
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_events (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 seq bigint NOT NULL,
 event_type text NOT NULL,
 entity_type text NOT NULL,
 entity_id text NOT NULL,
 entity_revision bigint NOT NULL,
 payload jsonb NOT NULL,
 UNIQUE(tenant_id, project_id, seq),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_cards (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 card_type text NOT NULL,
 entity_type text NOT NULL,
 entity_id text NOT NULL,
 status text NOT NULL,
 payload jsonb NOT NULL,
 allowed_actions jsonb NOT NULL,
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_outbox (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 job_id text,
 event_id text,
 message_type text NOT NULL,
 payload jsonb NOT NULL,
 delivery_status text NOT NULL,
 next_attempt_at timestamptz NOT NULL,
 attempt integer NOT NULL DEFAULT 0,
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_idempotency (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 actor_id text NOT NULL,
 route text NOT NULL,
 idempotency_key text NOT NULL,
 request_hash char(64) NOT NULL,
 resource_ref text,
 response_snapshot jsonb,
 expires_at timestamptz NOT NULL,
 UNIQUE(tenant_id, actor_id, route, idempotency_key),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_export_snapshots (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 input_fingerprint char(64) NOT NULL,
 versions jsonb NOT NULL,
 export_plan jsonb NOT NULL,
 content_review jsonb,
 layout_review jsonb,
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_export_plans (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 plan_revision bigint NOT NULL,
 plan jsonb NOT NULL,
 confirmed_by text,
 confirmed_at timestamptz,
 UNIQUE(tenant_id, project_id, plan_revision),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_exports (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 snapshot_id text NOT NULL,
 mode text NOT NULL CHECK(mode IN ('draft','final')),
 status text NOT NULL,
 artifacts jsonb NOT NULL DEFAULT '[]',
 manifest jsonb NOT NULL DEFAULT '{}',
 layout_review_status text NOT NULL,
 FOREIGN KEY(tenant_id, project_id, snapshot_id) REFERENCES bid_export_snapshots(tenant_id, project_id, id),
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE TABLE bid_audit_logs (
 tenant_id text NOT NULL,
 project_id text NOT NULL,
 id text NOT NULL,
 revision bigint NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 actor_id text NOT NULL,
 action text NOT NULL,
 target_type text NOT NULL,
 target_id text NOT NULL,
 target_revision bigint NOT NULL,
 reason text,
 trace_id text NOT NULL,
 details jsonb NOT NULL,
 PRIMARY KEY(tenant_id, project_id, id),
 FOREIGN KEY(tenant_id, project_id) REFERENCES bid_projects(tenant_id, id)
);

CREATE INDEX bid_jobs_claim_idx ON bid_jobs(status, lease_until, created_at);

CREATE INDEX bid_outbox_delivery_idx ON bid_outbox(delivery_status, next_attempt_at);

CREATE INDEX bid_requirements_scope_idx ON bid_requirements(tenant_id, project_id, analysis_id, scope_mode);

CREATE INDEX bid_dependencies_from_idx ON bid_dependencies(tenant_id, project_id, from_type, from_id, from_version);

-- 会话创建之前的幂等需要复用宿主请求幂等设施或独立不带project_id的命令表。

-- 不能先创建第二个project再依赖本表去重；session唯一约束与事务冲突处理是最后防线。

-- material版本修订追加bid_material_versions行；旧binding引用原版本，不原地覆盖。
