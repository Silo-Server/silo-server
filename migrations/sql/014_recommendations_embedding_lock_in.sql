-- +goose Up
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_media_item_embeddings_hnsw;
DROP INDEX IF EXISTS public.idx_user_taste_profiles_hnsw;
DROP INDEX IF EXISTS public.idx_taste_clusters_embedding;

TRUNCATE TABLE
    public.media_item_embeddings,
    public.user_taste_profiles,
    public.user_taste_clusters,
    public.recommendation_cache;

ALTER TABLE public.media_item_embeddings
    ALTER COLUMN embedding TYPE public.vector(3072)
    USING embedding::public.vector(3072);

ALTER TABLE public.user_taste_profiles
    ALTER COLUMN embedding TYPE public.vector(3072)
    USING embedding::public.vector(3072);

ALTER TABLE public.user_taste_clusters
    ALTER COLUMN embedding TYPE public.vector(3072)
    USING embedding::public.vector(3072);

CREATE INDEX idx_media_item_embeddings_hnsw
    ON public.media_item_embeddings USING hnsw ((embedding::halfvec(3072)) halfvec_cosine_ops);

CREATE INDEX idx_user_taste_profiles_hnsw
    ON public.user_taste_profiles USING hnsw ((embedding::halfvec(3072)) halfvec_cosine_ops);

CREATE INDEX idx_taste_clusters_embedding
    ON public.user_taste_clusters USING hnsw ((embedding::halfvec(3072)) halfvec_cosine_ops);

DELETE FROM public.server_settings
WHERE key IN (
    'recommendations.embedding_lock',
    'recommendations.embedding_provider',
    'recommendations.embedding_dimensions',
    'recommendations.openai_api_key',
    'recommendations.openai_model'
);
-- +goose StatementEnd
