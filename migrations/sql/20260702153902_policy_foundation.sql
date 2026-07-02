-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.policy_documents (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    domain text NOT NULL,
    name text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    active_version_id bigint,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT policy_documents_domain_check
        CHECK (domain IN ('scope', 'permission', 'action')),
    CONSTRAINT policy_documents_domain_name_key UNIQUE (domain, name)
);

-- Only one enabled document may target a silo_custom.<domain> package because
-- multiple enabled documents would define override twice; if both clauses
-- matched one input, OPA would report a runtime conflict and fail closed.
CREATE UNIQUE INDEX policy_documents_one_enabled_per_domain_idx
    ON public.policy_documents (domain)
    WHERE enabled;
COMMENT ON INDEX public.policy_documents_one_enabled_per_domain_idx IS
    'Only one enabled document may target a silo_custom.<domain> package; multiple enabled documents would define override twice, and matching clauses could cause an OPA runtime conflict that fails closed.';

CREATE TABLE public.policy_document_versions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    document_id bigint NOT NULL
        REFERENCES public.policy_documents(id) ON DELETE CASCADE,
    version_number integer NOT NULL,
    rego_source text NOT NULL,
    source_sha256 text NOT NULL,
    compiled_ok boolean NOT NULL,
    compile_error text,
    created_by_user_id integer
        REFERENCES public.users(id) ON DELETE SET NULL,
    comment text,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT policy_document_versions_document_version_key
        UNIQUE (document_id, version_number)
);

CREATE INDEX idx_policy_document_versions_document_version_desc
    ON public.policy_document_versions (document_id, version_number DESC);

ALTER TABLE public.policy_documents
    ADD CONSTRAINT policy_documents_active_version_id_fkey
    FOREIGN KEY (active_version_id)
    REFERENCES public.policy_document_versions(id) ON DELETE SET NULL;

CREATE TABLE public.policy_generation (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    generation bigint NOT NULL DEFAULT 1,
    updated_at timestamp with time zone NOT NULL DEFAULT now()
);

INSERT INTO public.policy_generation (id, generation)
VALUES (true, 1);

CREATE TABLE public.policy_decisions (
    id bigint GENERATED ALWAYS AS IDENTITY,
    "timestamp" timestamp with time zone NOT NULL DEFAULT now(),
    decision_name text NOT NULL,
    policy_generation bigint NOT NULL,
    user_id integer,
    profile_id text,
    session_id text,
    request_id text,
    node_id text,
    allowed boolean,
    eval_time_ns bigint NOT NULL,
    input_digest text NOT NULL,
    input_sample jsonb,
    result_sample jsonb,
    error text,
    PRIMARY KEY (id, "timestamp")
) PARTITION BY RANGE ("timestamp");

CREATE TABLE public.policy_decisions_default
    PARTITION OF public.policy_decisions DEFAULT;

CREATE INDEX idx_policy_decisions_timestamp_id
    ON public.policy_decisions ("timestamp" DESC, id DESC);
CREATE INDEX idx_policy_decisions_name_timestamp
    ON public.policy_decisions (decision_name, "timestamp" DESC);
CREATE INDEX idx_policy_decisions_user_timestamp
    ON public.policy_decisions (user_id, "timestamp" DESC)
    WHERE user_id IS NOT NULL;
CREATE INDEX idx_policy_decisions_denied_timestamp
    ON public.policy_decisions ("timestamp" DESC)
    WHERE allowed = false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.policy_decisions CASCADE;
DROP TABLE IF EXISTS public.policy_generation;
ALTER TABLE IF EXISTS public.policy_documents
    DROP CONSTRAINT IF EXISTS policy_documents_active_version_id_fkey;
DROP TABLE IF EXISTS public.policy_document_versions;
DROP TABLE IF EXISTS public.policy_documents;
-- +goose StatementEnd
