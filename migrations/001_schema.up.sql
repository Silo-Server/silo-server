--
-- PostgreSQL database dump
--


-- Dumped from database version 18.3 (Debian 18.3-1.pgdg12+1)
-- Dumped by pg_dump version 18.3 (Debian 18.3-1.pgdg12+1)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: vector; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: activity_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.activity_log (
    id bigint NOT NULL,
    "timestamp" timestamp with time zone DEFAULT now() NOT NULL,
    client_ip inet NOT NULL,
    user_id integer,
    session_id text,
    method text NOT NULL,
    path text NOT NULL,
    status_code integer,
    user_agent text,
    duration_ms integer
);


--
-- Name: activity_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.activity_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: activity_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.activity_log_id_seq OWNED BY public.activity_log.id;


--
-- Name: admin_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.admin_jobs (
    id text NOT NULL,
    job_type text NOT NULL,
    status text NOT NULL,
    created_by_user_id integer NOT NULL,
    request_payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    result_payload jsonb DEFAULT '{}'::jsonb NOT NULL,
    message text DEFAULT ''::text NOT NULL,
    error_message text DEFAULT ''::text NOT NULL,
    progress_current integer DEFAULT 0 NOT NULL,
    progress_total integer DEFAULT 0 NOT NULL,
    artifact_bucket text DEFAULT ''::text NOT NULL,
    artifact_key text DEFAULT ''::text NOT NULL,
    artifact_size_bytes bigint DEFAULT 0 NOT NULL,
    requested_at timestamp with time zone DEFAULT now() NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    heartbeat_at timestamp with time zone,
    expires_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT admin_jobs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'running'::text, 'completed'::text, 'failed'::text])))
);


--
-- Name: api_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.api_keys (
    id bigint NOT NULL,
    user_id integer NOT NULL,
    label text NOT NULL,
    api_key text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    rate_tier text DEFAULT 'standard'::text NOT NULL,
    CONSTRAINT chk_api_keys_rate_tier CHECK ((rate_tier = ANY (ARRAY['standard'::text, 'elevated'::text])))
);


--
-- Name: api_keys_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.api_keys_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: api_keys_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.api_keys_id_seq OWNED BY public.api_keys.id;


--
-- Name: auth_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auth_sessions (
    id text NOT NULL,
    user_id integer NOT NULL,
    device_name text,
    ip_address inet,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    revoked_at timestamp with time zone
);


--
-- Name: downloaded_subtitles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.downloaded_subtitles (
    id integer NOT NULL,
    media_file_id integer NOT NULL,
    provider text NOT NULL,
    language text NOT NULL,
    format text NOT NULL,
    release_name text DEFAULT ''::text NOT NULL,
    s3_key text NOT NULL,
    score double precision DEFAULT 0 NOT NULL,
    hearing_impaired boolean DEFAULT false NOT NULL,
    downloaded_by integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: downloaded_subtitles_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.downloaded_subtitles_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: downloaded_subtitles_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.downloaded_subtitles_id_seq OWNED BY public.downloaded_subtitles.id;


--
-- Name: episodes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.episodes (
    content_id text NOT NULL,
    series_id text NOT NULL,
    season_number integer NOT NULL,
    episode_number integer NOT NULL,
    title text,
    overview text,
    air_date date,
    runtime integer,
    rating_imdb double precision,
    rating_tmdb double precision,
    still_path text,
    still_thumbhash text,
    metadata_s3_path text,
    metadata_etag text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    season_id text
);


--
-- Name: invite_codes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.invite_codes (
    id bigint NOT NULL,
    code text NOT NULL,
    label text DEFAULT ''::text NOT NULL,
    max_uses integer NOT NULL,
    use_count integer DEFAULT 0 NOT NULL,
    created_by bigint NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: invite_codes_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.invite_codes_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: invite_codes_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.invite_codes_id_seq OWNED BY public.invite_codes.id;


--
-- Name: item_cowatch; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.item_cowatch (
    item_id text NOT NULL,
    similar_item_id text NOT NULL,
    jaccard_score double precision NOT NULL,
    cowatch_count integer NOT NULL,
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: item_people; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.item_people (
    id bigint NOT NULL,
    content_id text NOT NULL,
    person_id bigint NOT NULL,
    kind smallint NOT NULL,
    "character" text DEFAULT ''::text NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL
);


--
-- Name: jellycompat_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.jellycompat_sessions (
    token text NOT NULL,
    username text NOT NULL,
    account_username text NOT NULL,
    profile_id text NOT NULL,
    profile_name text NOT NULL,
    pseudo_user_id uuid NOT NULL,
    streamapp_user_id integer NOT NULL,
    streamapp_access_token text NOT NULL,
    streamapp_refresh_token text NOT NULL,
    streamapp_token_expiry timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL
);


--
-- Name: library_collection_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.library_collection_items (
    collection_id text NOT NULL,
    media_item_id text NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    source_rank integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: library_collection_sync_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.library_collection_sync_runs (
    id text NOT NULL,
    collection_id text NOT NULL,
    status text NOT NULL,
    message text DEFAULT ''::text NOT NULL,
    items_added integer DEFAULT 0 NOT NULL,
    items_removed integer DEFAULT 0 NOT NULL,
    items_matched integer DEFAULT 0 NOT NULL,
    items_unmatched integer DEFAULT 0 NOT NULL,
    warnings jsonb DEFAULT '[]'::jsonb NOT NULL,
    started_at timestamp with time zone,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT library_collection_sync_runs_status_check CHECK ((status = ANY (ARRAY['running'::text, 'success'::text, 'failed'::text, 'warning'::text])))
);


--
-- Name: library_collections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.library_collections (
    id text NOT NULL,
    library_id integer NOT NULL,
    slug text NOT NULL,
    title text NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    collection_type text NOT NULL,
    visibility text DEFAULT 'visible'::text NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    featured boolean DEFAULT false NOT NULL,
    poster_url text DEFAULT ''::text NOT NULL,
    backdrop_url text DEFAULT ''::text NOT NULL,
    source_url text DEFAULT ''::text NOT NULL,
    source_config jsonb DEFAULT '{}'::jsonb NOT NULL,
    last_sync_status text DEFAULT 'idle'::text NOT NULL,
    last_sync_message text DEFAULT ''::text NOT NULL,
    last_sync_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT library_collections_collection_type_check CHECK ((collection_type = ANY (ARRAY['manual'::text, 'mdblist'::text, 'kometa'::text]))),
    CONSTRAINT library_collections_last_sync_status_check CHECK ((last_sync_status = ANY (ARRAY['idle'::text, 'running'::text, 'success'::text, 'failed'::text, 'warning'::text]))),
    CONSTRAINT library_collections_visibility_check CHECK ((visibility = ANY (ARRAY['visible'::text, 'hidden'::text])))
);


--
-- Name: library_provider_chains; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.library_provider_chains (
    media_folder_id integer NOT NULL,
    provider_id integer NOT NULL,
    priority integer NOT NULL
);


--
-- Name: media_files; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.media_files (
    id integer NOT NULL,
    content_id text,
    media_folder_id integer NOT NULL,
    file_path text NOT NULL,
    file_size bigint,
    file_hash text,
    codec_video text,
    codec_audio text,
    resolution text,
    audio_channels integer,
    hdr boolean,
    container text,
    duration bigint,
    bitrate integer,
    subtitle_tracks jsonb,
    external_subtitles jsonb,
    intro_start double precision,
    intro_end double precision,
    credits_start double precision,
    credits_end double precision,
    probe_source text,
    probe_updated_at timestamp with time zone,
    missing_since timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    episode_id text,
    season_number integer,
    episode_number integer,
    video_tracks jsonb,
    audio_tracks jsonb
);


--
-- Name: media_files_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.media_files_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: media_files_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.media_files_id_seq OWNED BY public.media_files.id;


--
-- Name: media_folder_paths; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.media_folder_paths (
    id integer NOT NULL,
    media_folder_id integer NOT NULL,
    path text NOT NULL
);


--
-- Name: media_folder_paths_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.media_folder_paths_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: media_folder_paths_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.media_folder_paths_id_seq OWNED BY public.media_folder_paths.id;


--
-- Name: media_folders; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.media_folders (
    id integer NOT NULL,
    type text NOT NULL,
    name text NOT NULL,
    enabled boolean DEFAULT true,
    last_scanned_at timestamp with time zone,
    scan_warning_code text,
    scan_warning_message text,
    scan_warning_at timestamp with time zone,
    allow_empty_cleanup_once boolean DEFAULT false NOT NULL,
    poster_path text DEFAULT ''::text NOT NULL
);


--
-- Name: media_folders_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.media_folders_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: media_folders_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.media_folders_id_seq OWNED BY public.media_folders.id;


--
-- Name: media_item_embeddings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.media_item_embeddings (
    media_item_id text NOT NULL,
    embedding public.vector(1536) NOT NULL,
    model text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    canonical_text text DEFAULT ''::text NOT NULL
);


--
-- Name: media_item_libraries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.media_item_libraries (
    content_id text NOT NULL,
    media_folder_id integer NOT NULL,
    first_seen_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: media_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.media_items (
    content_id text NOT NULL,
    type text NOT NULL,
    title text NOT NULL,
    sort_title text,
    original_title text,
    year integer,
    genres text[],
    content_rating text,
    runtime integer,
    overview text,
    tagline text,
    rating_imdb double precision,
    rating_tmdb double precision,
    rating_rt_critic integer,
    rating_rt_audience integer,
    imdb_id text,
    tmdb_id text,
    tvdb_id text,
    poster_path text,
    poster_thumbhash text,
    backdrop_path text,
    backdrop_thumbhash text,
    logo_path text,
    metadata_s3_path text,
    metadata_etag text,
    season_count integer,
    matched_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    studios text[] DEFAULT '{}'::text[] NOT NULL,
    networks text[] DEFAULT '{}'::text[] NOT NULL,
    countries text[] DEFAULT '{}'::text[] NOT NULL,
    first_air_date text,
    last_air_date text,
    last_refreshed timestamp with time zone,
    refresh_failures integer DEFAULT 0 NOT NULL,
    locked_fields integer[] DEFAULT '{}'::integer[] NOT NULL,
    status text DEFAULT 'matched'::text NOT NULL
);


--
-- Name: metadata_providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.metadata_providers (
    id integer NOT NULL,
    slug text NOT NULL,
    provider_type text NOT NULL,
    enabled boolean DEFAULT true,
    settings jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: metadata_providers_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.metadata_providers_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: metadata_providers_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.metadata_providers_id_seq OWNED BY public.metadata_providers.id;


--
-- Name: page_sections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.page_sections (
    id text NOT NULL,
    scope text NOT NULL,
    library_id bigint,
    "position" integer DEFAULT 0 NOT NULL,
    section_type text NOT NULL,
    title text NOT NULL,
    featured boolean DEFAULT false NOT NULL,
    item_limit integer DEFAULT 20 NOT NULL,
    config jsonb DEFAULT '{}'::jsonb NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT page_sections_library_scope CHECK ((((scope = 'home'::text) AND (library_id IS NULL)) OR ((scope = 'library'::text) AND (library_id IS NOT NULL)))),
    CONSTRAINT page_sections_scope_check CHECK ((scope = ANY (ARRAY['home'::text, 'library'::text])))
);


--
-- Name: people; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.people (
    id bigint NOT NULL,
    name text NOT NULL,
    sort_name text DEFAULT ''::text NOT NULL,
    bio text DEFAULT ''::text NOT NULL,
    birth_date date,
    death_date date,
    birthplace text DEFAULT ''::text NOT NULL,
    homepage text DEFAULT ''::text NOT NULL,
    photo_path text DEFAULT ''::text NOT NULL,
    photo_thumbhash text DEFAULT ''::text NOT NULL,
    tmdb_id text DEFAULT ''::text NOT NULL,
    imdb_id text DEFAULT ''::text NOT NULL,
    tvdb_id text DEFAULT ''::text NOT NULL,
    plex_guid text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: playback_history_admin; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.playback_history_admin (
    session_id text NOT NULL,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    profile_name text DEFAULT ''::text NOT NULL,
    media_item_id text DEFAULT ''::text NOT NULL,
    media_file_id integer NOT NULL,
    play_method text NOT NULL,
    started_at timestamp with time zone NOT NULL,
    ended_at timestamp with time zone NOT NULL,
    watched_seconds double precision DEFAULT 0 NOT NULL,
    duration_seconds double precision,
    completed boolean DEFAULT false NOT NULL,
    client_ip inet
);


--
-- Name: playback_sessions_sync; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.playback_sessions_sync (
    session_id text NOT NULL,
    user_id integer NOT NULL,
    profile_id text,
    media_file_id integer,
    play_method text,
    reporting_node text,
    started_at timestamp with time zone,
    updated_at timestamp with time zone,
    last_sync_at timestamp with time zone
);


--
-- Name: recommendation_cache; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.recommendation_cache (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    rec_type text NOT NULL,
    source_item_id text DEFAULT ''::text NOT NULL,
    items jsonb DEFAULT '[]'::jsonb NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: seasons; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.seasons (
    content_id text NOT NULL,
    series_id text NOT NULL,
    season_number integer NOT NULL,
    title text,
    overview text,
    air_date date,
    poster_path text,
    poster_thumbhash text,
    metadata_s3_path text,
    metadata_etag text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: server_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.server_settings (
    key text NOT NULL,
    value text NOT NULL
);


--
-- Name: stream_nodes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.stream_nodes (
    id integer NOT NULL,
    name text NOT NULL,
    type text NOT NULL,
    url text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    healthy boolean DEFAULT false NOT NULL,
    active_jobs integer DEFAULT 0 NOT NULL,
    last_health_check timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT stream_nodes_type_check CHECK ((type = ANY (ARRAY['proxy'::text, 'transcode'::text])))
);


--
-- Name: stream_nodes_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.stream_nodes_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: stream_nodes_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.stream_nodes_id_seq OWNED BY public.stream_nodes.id;


--
-- Name: subtitle_provider_config; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.subtitle_provider_config (
    provider_name text NOT NULL,
    enabled boolean DEFAULT false NOT NULL,
    api_key text DEFAULT ''::text NOT NULL,
    username text DEFAULT ''::text NOT NULL,
    password text DEFAULT ''::text NOT NULL,
    extra_config jsonb,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: user_aggregates; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_aggregates (
    user_id integer NOT NULL,
    total_watched integer DEFAULT 0,
    favorites_count integer DEFAULT 0,
    watchlist_count integer DEFAULT 0,
    active_node text,
    last_sync_at timestamp with time zone
);


--
-- Name: user_audio_preferences; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_audio_preferences (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    series_id text NOT NULL,
    audio_track_index integer,
    audio_language text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: user_downloads; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_downloads (
    id text NOT NULL,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    media_item_id text NOT NULL,
    media_file_id integer NOT NULL,
    quality text DEFAULT ''::text NOT NULL,
    transcoded boolean DEFAULT false NOT NULL,
    file_size bigint,
    expires_at timestamp with time zone,
    downloaded_at timestamp with time zone
);


--
-- Name: user_favorites; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_favorites (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    media_item_id text NOT NULL,
    added_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: user_personal_collection_items; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_personal_collection_items (
    user_id integer NOT NULL,
    collection_id text NOT NULL,
    media_item_id text NOT NULL,
    "position" integer DEFAULT 0 NOT NULL,
    added_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: user_personal_collections; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_personal_collections (
    id text NOT NULL,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    name text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: user_playback_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_playback_sessions (
    session_id text NOT NULL,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    media_file_id integer NOT NULL,
    play_method text NOT NULL,
    position_seconds double precision DEFAULT 0 NOT NULL,
    is_paused boolean DEFAULT false NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: user_profile_allowed_libraries; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_profile_allowed_libraries (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    library_id integer NOT NULL
);


--
-- Name: user_profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_profiles (
    id text NOT NULL,
    user_id integer NOT NULL,
    name text NOT NULL,
    avatar text DEFAULT ''::text NOT NULL,
    pin_hash text DEFAULT ''::text NOT NULL,
    is_child boolean DEFAULT false NOT NULL,
    max_content_rating text DEFAULT ''::text NOT NULL,
    quality_preference text DEFAULT '1080p'::text NOT NULL,
    language text DEFAULT 'en'::text NOT NULL,
    subtitle_language text DEFAULT ''::text NOT NULL,
    subtitle_mode text DEFAULT 'auto'::text NOT NULL,
    auto_skip_intro boolean DEFAULT false NOT NULL,
    auto_skip_credits boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    library_restrictions_enabled boolean DEFAULT false NOT NULL,
    max_playback_quality text DEFAULT ''::text NOT NULL
);


--
-- Name: user_ratings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_ratings (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    media_item_id text NOT NULL,
    rating smallint NOT NULL,
    rated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_ratings_rating_check CHECK (((rating >= 1) AND (rating <= 5)))
);


--
-- Name: user_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_settings (
    user_id integer NOT NULL,
    key text NOT NULL,
    value text NOT NULL
);

CREATE TABLE public.user_device_settings (
    user_id integer NOT NULL,
    device_id text NOT NULL,
    key text NOT NULL,
    value text NOT NULL,
    device_name text DEFAULT ''::text NOT NULL,
    device_platform text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: user_subtitle_preferences; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_subtitle_preferences (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    series_id text NOT NULL,
    subtitle_language text DEFAULT ''::text NOT NULL,
    subtitle_track_index integer DEFAULT 0 NOT NULL,
    external_subtitle_path text DEFAULT ''::text NOT NULL,
    subtitle_mode text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: user_taste_clusters; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_taste_clusters (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    cluster_idx integer NOT NULL,
    embedding public.vector(768),
    dominant_genres jsonb,
    label text,
    member_count integer,
    total_weight double precision,
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: user_taste_profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_taste_profiles (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    embedding public.vector(1536) NOT NULL,
    signal_counts jsonb DEFAULT '{}'::jsonb NOT NULL,
    max_content_rating text DEFAULT ''::text NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    stale_at timestamp with time zone
);


--
-- Name: user_watch_history; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_watch_history (
    id text NOT NULL,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    media_item_id text NOT NULL,
    watched_at timestamp with time zone DEFAULT now() NOT NULL,
    duration_seconds double precision,
    completed boolean DEFAULT false NOT NULL
);


--
-- Name: user_watch_progress; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_watch_progress (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    media_item_id text NOT NULL,
    position_seconds double precision DEFAULT 0 NOT NULL,
    duration_seconds double precision DEFAULT 0 NOT NULL,
    completed boolean DEFAULT false NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    last_file_id integer,
    last_resolution text,
    last_hdr boolean,
    last_codec_video text
);


--
-- Name: user_watchlist; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.user_watchlist (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    media_item_id text NOT NULL,
    added_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id integer NOT NULL,
    email text,
    username text,
    password_hash text,
    role text,
    permissions text[] DEFAULT '{}'::text[] NOT NULL,
    enabled boolean DEFAULT true,
    library_ids integer[],
    max_streams integer DEFAULT 6,
    max_transcodes integer DEFAULT 2,
    download_allowed boolean DEFAULT true,
    download_transcode_allowed boolean DEFAULT false,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    max_playback_quality text DEFAULT ''::text NOT NULL,
    access_policy_revision bigint DEFAULT 1 NOT NULL
);


--
-- Name: users_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.users_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: users_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.users_id_seq OWNED BY public.users.id;


--
-- Name: activity_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.activity_log ALTER COLUMN id SET DEFAULT nextval('public.activity_log_id_seq'::regclass);


--
-- Name: api_keys id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys ALTER COLUMN id SET DEFAULT nextval('public.api_keys_id_seq'::regclass);


--
-- Name: downloaded_subtitles id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.downloaded_subtitles ALTER COLUMN id SET DEFAULT nextval('public.downloaded_subtitles_id_seq'::regclass);


--
-- Name: invite_codes id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.invite_codes ALTER COLUMN id SET DEFAULT nextval('public.invite_codes_id_seq'::regclass);


--
-- Name: media_files id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_files ALTER COLUMN id SET DEFAULT nextval('public.media_files_id_seq'::regclass);


--
-- Name: media_folder_paths id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_folder_paths ALTER COLUMN id SET DEFAULT nextval('public.media_folder_paths_id_seq'::regclass);


--
-- Name: media_folders id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_folders ALTER COLUMN id SET DEFAULT nextval('public.media_folders_id_seq'::regclass);


--
-- Name: metadata_providers id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.metadata_providers ALTER COLUMN id SET DEFAULT nextval('public.metadata_providers_id_seq'::regclass);


--
-- Name: stream_nodes id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stream_nodes ALTER COLUMN id SET DEFAULT nextval('public.stream_nodes_id_seq'::regclass);


--
-- Name: users id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users ALTER COLUMN id SET DEFAULT nextval('public.users_id_seq'::regclass);


--
-- Name: activity_log activity_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.activity_log
    ADD CONSTRAINT activity_log_pkey PRIMARY KEY (id);


--
-- Name: admin_jobs admin_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.admin_jobs
    ADD CONSTRAINT admin_jobs_pkey PRIMARY KEY (id);


--
-- Name: api_keys api_keys_api_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_api_key_key UNIQUE (api_key);


--
-- Name: api_keys api_keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_pkey PRIMARY KEY (id);


--
-- Name: auth_sessions auth_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_sessions
    ADD CONSTRAINT auth_sessions_pkey PRIMARY KEY (id);


--
-- Name: downloaded_subtitles downloaded_subtitles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.downloaded_subtitles
    ADD CONSTRAINT downloaded_subtitles_pkey PRIMARY KEY (id);


--
-- Name: downloaded_subtitles downloaded_subtitles_s3_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.downloaded_subtitles
    ADD CONSTRAINT downloaded_subtitles_s3_key_key UNIQUE (s3_key);


--
-- Name: episodes episodes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.episodes
    ADD CONSTRAINT episodes_pkey PRIMARY KEY (content_id);


--
-- Name: episodes episodes_series_season_episode_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.episodes
    ADD CONSTRAINT episodes_series_season_episode_key UNIQUE (series_id, season_number, episode_number);


--
-- Name: invite_codes invite_codes_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.invite_codes
    ADD CONSTRAINT invite_codes_code_key UNIQUE (code);


--
-- Name: invite_codes invite_codes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.invite_codes
    ADD CONSTRAINT invite_codes_pkey PRIMARY KEY (id);


--
-- Name: item_cowatch item_cowatch_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.item_cowatch
    ADD CONSTRAINT item_cowatch_pkey PRIMARY KEY (item_id, similar_item_id);


--
-- Name: item_people item_people_content_id_person_id_kind_character_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.item_people
    ADD CONSTRAINT item_people_content_id_person_id_kind_character_key UNIQUE (content_id, person_id, kind, "character");


--
-- Name: item_people item_people_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.item_people
    ADD CONSTRAINT item_people_pkey PRIMARY KEY (id);


--
-- Name: jellycompat_sessions jellycompat_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.jellycompat_sessions
    ADD CONSTRAINT jellycompat_sessions_pkey PRIMARY KEY (token);


--
-- Name: library_collection_items library_collection_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_collection_items
    ADD CONSTRAINT library_collection_items_pkey PRIMARY KEY (collection_id, media_item_id);


--
-- Name: library_collection_sync_runs library_collection_sync_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_collection_sync_runs
    ADD CONSTRAINT library_collection_sync_runs_pkey PRIMARY KEY (id);


--
-- Name: library_collections library_collections_library_slug_unique; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_collections
    ADD CONSTRAINT library_collections_library_slug_unique UNIQUE (library_id, slug);


--
-- Name: library_collections library_collections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_collections
    ADD CONSTRAINT library_collections_pkey PRIMARY KEY (id);


--
-- Name: library_provider_chains library_provider_chains_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_provider_chains
    ADD CONSTRAINT library_provider_chains_pkey PRIMARY KEY (media_folder_id, provider_id);


--
-- Name: media_files media_files_file_path_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_files
    ADD CONSTRAINT media_files_file_path_key UNIQUE (file_path);


--
-- Name: media_files media_files_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_files
    ADD CONSTRAINT media_files_pkey PRIMARY KEY (id);


--
-- Name: media_folder_paths media_folder_paths_path_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_folder_paths
    ADD CONSTRAINT media_folder_paths_path_key UNIQUE (path);


--
-- Name: media_folder_paths media_folder_paths_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_folder_paths
    ADD CONSTRAINT media_folder_paths_pkey PRIMARY KEY (id);


--
-- Name: media_folders media_folders_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_folders
    ADD CONSTRAINT media_folders_pkey PRIMARY KEY (id);


--
-- Name: media_item_embeddings media_item_embeddings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_item_embeddings
    ADD CONSTRAINT media_item_embeddings_pkey PRIMARY KEY (media_item_id);


--
-- Name: media_item_libraries media_item_libraries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_item_libraries
    ADD CONSTRAINT media_item_libraries_pkey PRIMARY KEY (content_id, media_folder_id);


--
-- Name: media_items media_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_items
    ADD CONSTRAINT media_items_pkey PRIMARY KEY (content_id);


--
-- Name: metadata_providers metadata_providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.metadata_providers
    ADD CONSTRAINT metadata_providers_pkey PRIMARY KEY (id);


--
-- Name: metadata_providers metadata_providers_slug_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.metadata_providers
    ADD CONSTRAINT metadata_providers_slug_key UNIQUE (slug);


--
-- Name: page_sections page_sections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.page_sections
    ADD CONSTRAINT page_sections_pkey PRIMARY KEY (id);


--
-- Name: people people_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.people
    ADD CONSTRAINT people_pkey PRIMARY KEY (id);


--
-- Name: playback_history_admin playback_history_admin_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.playback_history_admin
    ADD CONSTRAINT playback_history_admin_pkey PRIMARY KEY (session_id);


--
-- Name: playback_sessions_sync playback_sessions_sync_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.playback_sessions_sync
    ADD CONSTRAINT playback_sessions_sync_pkey PRIMARY KEY (session_id);


--
-- Name: recommendation_cache recommendation_cache_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.recommendation_cache
    ADD CONSTRAINT recommendation_cache_pkey PRIMARY KEY (user_id, profile_id, rec_type, source_item_id);


--
-- Name: seasons seasons_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.seasons
    ADD CONSTRAINT seasons_pkey PRIMARY KEY (content_id);


--
-- Name: seasons seasons_series_id_season_number_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.seasons
    ADD CONSTRAINT seasons_series_id_season_number_key UNIQUE (series_id, season_number);


--
-- Name: server_settings server_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.server_settings
    ADD CONSTRAINT server_settings_pkey PRIMARY KEY (key);


--
-- Name: stream_nodes stream_nodes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stream_nodes
    ADD CONSTRAINT stream_nodes_pkey PRIMARY KEY (id);


--
-- Name: stream_nodes stream_nodes_url_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.stream_nodes
    ADD CONSTRAINT stream_nodes_url_key UNIQUE (url);


--
-- Name: subtitle_provider_config subtitle_provider_config_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.subtitle_provider_config
    ADD CONSTRAINT subtitle_provider_config_pkey PRIMARY KEY (provider_name);


--
-- Name: user_aggregates user_aggregates_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_aggregates
    ADD CONSTRAINT user_aggregates_pkey PRIMARY KEY (user_id);


--
-- Name: user_audio_preferences user_audio_preferences_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_audio_preferences
    ADD CONSTRAINT user_audio_preferences_pkey PRIMARY KEY (user_id, profile_id, series_id);


--
-- Name: user_downloads user_downloads_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_downloads
    ADD CONSTRAINT user_downloads_pkey PRIMARY KEY (user_id, id);


--
-- Name: user_favorites user_favorites_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_favorites
    ADD CONSTRAINT user_favorites_pkey PRIMARY KEY (user_id, profile_id, media_item_id);


--
-- Name: user_personal_collection_items user_personal_collection_items_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_personal_collection_items
    ADD CONSTRAINT user_personal_collection_items_pkey PRIMARY KEY (user_id, collection_id, media_item_id);


--
-- Name: user_personal_collections user_personal_collections_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_personal_collections
    ADD CONSTRAINT user_personal_collections_pkey PRIMARY KEY (user_id, id);


--
-- Name: user_playback_sessions user_playback_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_playback_sessions
    ADD CONSTRAINT user_playback_sessions_pkey PRIMARY KEY (user_id, session_id);


--
-- Name: user_profile_allowed_libraries user_profile_allowed_libraries_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_profile_allowed_libraries
    ADD CONSTRAINT user_profile_allowed_libraries_pkey PRIMARY KEY (user_id, profile_id, library_id);


--
-- Name: user_profiles user_profiles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_profiles
    ADD CONSTRAINT user_profiles_pkey PRIMARY KEY (user_id, id);


--
-- Name: user_ratings user_ratings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_ratings
    ADD CONSTRAINT user_ratings_pkey PRIMARY KEY (user_id, profile_id, media_item_id);


--
-- Name: user_settings user_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_settings
    ADD CONSTRAINT user_settings_pkey PRIMARY KEY (user_id, key);

ALTER TABLE ONLY public.user_device_settings
    ADD CONSTRAINT user_device_settings_pkey PRIMARY KEY (user_id, device_id, key);


--
-- Name: user_subtitle_preferences user_subtitle_preferences_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_subtitle_preferences
    ADD CONSTRAINT user_subtitle_preferences_pkey PRIMARY KEY (user_id, profile_id, series_id);


--
-- Name: user_taste_clusters user_taste_clusters_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_taste_clusters
    ADD CONSTRAINT user_taste_clusters_pkey PRIMARY KEY (user_id, profile_id, cluster_idx);


--
-- Name: user_taste_profiles user_taste_profiles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_taste_profiles
    ADD CONSTRAINT user_taste_profiles_pkey PRIMARY KEY (user_id, profile_id);


--
-- Name: user_watch_history user_watch_history_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_watch_history
    ADD CONSTRAINT user_watch_history_pkey PRIMARY KEY (user_id, id);


--
-- Name: user_watch_progress user_watch_progress_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_watch_progress
    ADD CONSTRAINT user_watch_progress_pkey PRIMARY KEY (user_id, profile_id, media_item_id);


--
-- Name: user_watchlist user_watchlist_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_watchlist
    ADD CONSTRAINT user_watchlist_pkey PRIMARY KEY (user_id, profile_id, media_item_id);


--
-- Name: users users_email_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_email_key UNIQUE (email);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: users users_username_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_username_key UNIQUE (username);


--
-- Name: admin_jobs_active_job_type_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX admin_jobs_active_job_type_idx ON public.admin_jobs USING btree (job_type) WHERE (status = ANY (ARRAY['queued'::text, 'running'::text]));


--
-- Name: admin_jobs_expires_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX admin_jobs_expires_at_idx ON public.admin_jobs USING btree (expires_at) WHERE (expires_at IS NOT NULL);


--
-- Name: admin_jobs_lookup_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX admin_jobs_lookup_idx ON public.admin_jobs USING btree (job_type, status, requested_at DESC);


--
-- Name: idx_activity_log_client_ip; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_activity_log_client_ip ON public.activity_log USING btree (client_ip, "timestamp" DESC);


--
-- Name: idx_activity_log_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_activity_log_timestamp ON public.activity_log USING btree ("timestamp" DESC);


--
-- Name: idx_activity_log_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_activity_log_user_id ON public.activity_log USING btree (user_id, "timestamp" DESC);


--
-- Name: idx_api_keys_api_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_api_key ON public.api_keys USING btree (api_key);


--
-- Name: idx_api_keys_user_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_user_id ON public.api_keys USING btree (user_id);


--
-- Name: idx_cowatch_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cowatch_lookup ON public.item_cowatch USING btree (item_id, jaccard_score DESC);


--
-- Name: idx_downloaded_subtitles_lang; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_downloaded_subtitles_lang ON public.downloaded_subtitles USING btree (media_file_id, language);


--
-- Name: idx_downloaded_subtitles_media; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_downloaded_subtitles_media ON public.downloaded_subtitles USING btree (media_file_id);


--
-- Name: idx_episodes_series; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_episodes_series ON public.episodes USING btree (series_id, season_number, episode_number);


--
-- Name: idx_item_libraries_content; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_item_libraries_content ON public.media_item_libraries USING btree (content_id);


--
-- Name: idx_item_libraries_folder; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_item_libraries_folder ON public.media_item_libraries USING btree (media_folder_id, first_seen_at DESC);


--
-- Name: idx_item_people_content_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_item_people_content_id ON public.item_people USING btree (content_id);


--
-- Name: idx_item_people_person_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_item_people_person_id ON public.item_people USING btree (person_id);


--
-- Name: idx_library_collection_items_order; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_library_collection_items_order ON public.library_collection_items USING btree (collection_id, "position", source_rank, media_item_id);


--
-- Name: idx_library_collection_sync_runs_collection; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_library_collection_sync_runs_collection ON public.library_collection_sync_runs USING btree (collection_id, created_at DESC);


--
-- Name: idx_library_collections_library_order; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_library_collections_library_order ON public.library_collections USING btree (library_id, sort_order, title);


--
-- Name: idx_library_collections_visibility; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_library_collections_visibility ON public.library_collections USING btree (library_id, visibility, featured DESC, sort_order);


--
-- Name: idx_media_files_content; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_files_content ON public.media_files USING btree (content_id);


--
-- Name: idx_media_files_episode; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_files_episode ON public.media_files USING btree (episode_id) WHERE (episode_id IS NOT NULL);


--
-- Name: idx_media_files_folder; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_files_folder ON public.media_files USING btree (media_folder_id);


--
-- Name: idx_media_files_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_files_hash ON public.media_files USING btree (file_hash);


--
-- Name: idx_media_files_unlinked; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_files_unlinked ON public.media_files USING btree (content_id) WHERE ((content_id IS NOT NULL) AND (episode_id IS NULL));


--
-- Name: idx_media_folder_paths_folder; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_folder_paths_folder ON public.media_folder_paths USING btree (media_folder_id);


--
-- Name: idx_media_item_embeddings_hnsw; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_item_embeddings_hnsw ON public.media_item_embeddings USING hnsw (embedding public.vector_cosine_ops);


--
-- Name: idx_media_items_genres; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_items_genres ON public.media_items USING gin (genres);


--
-- Name: idx_media_items_last_refreshed; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_items_last_refreshed ON public.media_items USING btree (last_refreshed NULLS FIRST) WHERE (last_refreshed IS NOT NULL);


--
-- Name: idx_media_items_search; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_items_search ON public.media_items USING gin (to_tsvector('english'::regconfig, ((title || ' '::text) || COALESCE(overview, ''::text))));


--
-- Name: idx_media_items_search_exact_title; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_items_search_exact_title ON public.media_items USING btree (lower(title));


--
-- Name: idx_media_items_search_overview; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_items_search_overview ON public.media_items USING gin (to_tsvector('english'::regconfig, COALESCE(overview, ''::text)));


--
-- Name: idx_media_items_search_title_fields; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_items_search_title_fields ON public.media_items USING gin ((((setweight(to_tsvector('simple'::regconfig, COALESCE(title, ''::text)), 'A'::"char") || setweight(to_tsvector('simple'::regconfig, COALESCE(original_title, ''::text)), 'A'::"char")) || setweight(to_tsvector('simple'::regconfig, COALESCE(sort_title, ''::text)), 'B'::"char"))));


--
-- Name: idx_media_items_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_items_status ON public.media_items USING btree (status) WHERE (status <> 'matched'::text);


--
-- Name: idx_media_items_type_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_items_type_created ON public.media_items USING btree (type, created_at DESC);


--
-- Name: idx_media_items_type_rating_imdb; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_items_type_rating_imdb ON public.media_items USING btree (type, rating_imdb DESC NULLS LAST);


--
-- Name: idx_media_items_type_rating_tmdb; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_items_type_rating_tmdb ON public.media_items USING btree (type, rating_tmdb DESC NULLS LAST);


--
-- Name: idx_media_items_type_sort_title; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_items_type_sort_title ON public.media_items USING btree (type, sort_title);


--
-- Name: idx_media_items_type_year; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_media_items_type_year ON public.media_items USING btree (type, year DESC);


--
-- Name: idx_page_sections_library; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_page_sections_library ON public.page_sections USING btree (library_id, "position") WHERE ((enabled = true) AND (library_id IS NOT NULL));


--
-- Name: idx_page_sections_scope; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_page_sections_scope ON public.page_sections USING btree (scope, "position") WHERE (enabled = true);


--
-- Name: idx_people_imdb_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_people_imdb_id ON public.people USING btree (imdb_id) WHERE (imdb_id <> ''::text);


--
-- Name: idx_people_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_people_name ON public.people USING btree (name);


--
-- Name: idx_people_tmdb_id; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_people_tmdb_id ON public.people USING btree (tmdb_id) WHERE (tmdb_id <> ''::text);


--
-- Name: idx_playback_history_admin_ended; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_playback_history_admin_ended ON public.playback_history_admin USING btree (ended_at DESC);


--
-- Name: idx_playback_history_admin_user_ended; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_playback_history_admin_user_ended ON public.playback_history_admin USING btree (user_id, ended_at DESC);


--
-- Name: idx_playback_history_admin_user_profile_ended; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_playback_history_admin_user_profile_ended ON public.playback_history_admin USING btree (user_id, profile_id, ended_at DESC);


--
-- Name: idx_playback_sync_node; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_playback_sync_node ON public.playback_sessions_sync USING btree (reporting_node, last_sync_at);


--
-- Name: idx_seasons_series; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_seasons_series ON public.seasons USING btree (series_id, season_number);


--
-- Name: idx_taste_clusters_embedding; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_taste_clusters_embedding ON public.user_taste_clusters USING hnsw (embedding public.vector_cosine_ops);


--
-- Name: idx_user_collections_profile; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_collections_profile ON public.user_personal_collections USING btree (user_id, profile_id);


--
-- Name: idx_user_favorites_profile; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_favorites_profile ON public.user_favorites USING btree (user_id, profile_id, added_at DESC);


--
-- Name: idx_user_profile_allowed_libraries_lookup; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_profile_allowed_libraries_lookup ON public.user_profile_allowed_libraries USING btree (user_id, profile_id);


--
-- Name: idx_user_ratings_profile; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_ratings_profile ON public.user_ratings USING btree (user_id, profile_id);


--
-- Name: idx_user_taste_profiles_hnsw; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_taste_profiles_hnsw ON public.user_taste_profiles USING hnsw (embedding public.vector_cosine_ops);


--
-- Name: idx_user_watch_history_profile; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_watch_history_profile ON public.user_watch_history USING btree (user_id, profile_id, watched_at DESC);


--
-- Name: idx_user_watch_progress_profile; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_watch_progress_profile ON public.user_watch_progress USING btree (user_id, profile_id);


--
-- Name: idx_user_watchlist_profile; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_user_watchlist_profile ON public.user_watchlist USING btree (user_id, profile_id, added_at DESC);


--
-- Name: jellycompat_sessions_expires_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX jellycompat_sessions_expires_at_idx ON public.jellycompat_sessions USING btree (expires_at);


--
-- Name: activity_log activity_log_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.activity_log
    ADD CONSTRAINT activity_log_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: api_keys api_keys_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: auth_sessions auth_sessions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auth_sessions
    ADD CONSTRAINT auth_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: downloaded_subtitles downloaded_subtitles_downloaded_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.downloaded_subtitles
    ADD CONSTRAINT downloaded_subtitles_downloaded_by_fkey FOREIGN KEY (downloaded_by) REFERENCES public.users(id) ON DELETE SET NULL;


--
-- Name: downloaded_subtitles downloaded_subtitles_media_file_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.downloaded_subtitles
    ADD CONSTRAINT downloaded_subtitles_media_file_id_fkey FOREIGN KEY (media_file_id) REFERENCES public.media_files(id) ON DELETE CASCADE;


--
-- Name: episodes episodes_season_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.episodes
    ADD CONSTRAINT episodes_season_id_fkey FOREIGN KEY (season_id) REFERENCES public.seasons(content_id) ON DELETE CASCADE;


--
-- Name: episodes episodes_series_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.episodes
    ADD CONSTRAINT episodes_series_id_fkey FOREIGN KEY (series_id) REFERENCES public.media_items(content_id) ON DELETE CASCADE;


--
-- Name: invite_codes invite_codes_created_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.invite_codes
    ADD CONSTRAINT invite_codes_created_by_fkey FOREIGN KEY (created_by) REFERENCES public.users(id);


--
-- Name: item_people item_people_content_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.item_people
    ADD CONSTRAINT item_people_content_id_fkey FOREIGN KEY (content_id) REFERENCES public.media_items(content_id) ON DELETE CASCADE;


--
-- Name: item_people item_people_person_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.item_people
    ADD CONSTRAINT item_people_person_id_fkey FOREIGN KEY (person_id) REFERENCES public.people(id) ON DELETE CASCADE;


--
-- Name: library_collection_items library_collection_items_collection_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_collection_items
    ADD CONSTRAINT library_collection_items_collection_id_fkey FOREIGN KEY (collection_id) REFERENCES public.library_collections(id) ON DELETE CASCADE;


--
-- Name: library_collection_items library_collection_items_media_item_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_collection_items
    ADD CONSTRAINT library_collection_items_media_item_id_fkey FOREIGN KEY (media_item_id) REFERENCES public.media_items(content_id) ON DELETE CASCADE;


--
-- Name: library_collection_sync_runs library_collection_sync_runs_collection_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_collection_sync_runs
    ADD CONSTRAINT library_collection_sync_runs_collection_id_fkey FOREIGN KEY (collection_id) REFERENCES public.library_collections(id) ON DELETE CASCADE;


--
-- Name: library_collections library_collections_library_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_collections
    ADD CONSTRAINT library_collections_library_id_fkey FOREIGN KEY (library_id) REFERENCES public.media_folders(id) ON DELETE CASCADE;


--
-- Name: library_provider_chains library_provider_chains_media_folder_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_provider_chains
    ADD CONSTRAINT library_provider_chains_media_folder_id_fkey FOREIGN KEY (media_folder_id) REFERENCES public.media_folders(id) ON DELETE CASCADE;


--
-- Name: library_provider_chains library_provider_chains_provider_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.library_provider_chains
    ADD CONSTRAINT library_provider_chains_provider_id_fkey FOREIGN KEY (provider_id) REFERENCES public.metadata_providers(id) ON DELETE CASCADE;


--
-- Name: media_files media_files_episode_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_files
    ADD CONSTRAINT media_files_episode_id_fkey FOREIGN KEY (episode_id) REFERENCES public.episodes(content_id) ON DELETE SET NULL;


--
-- Name: media_files media_files_media_folder_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_files
    ADD CONSTRAINT media_files_media_folder_id_fkey FOREIGN KEY (media_folder_id) REFERENCES public.media_folders(id) ON DELETE CASCADE;


--
-- Name: media_folder_paths media_folder_paths_media_folder_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_folder_paths
    ADD CONSTRAINT media_folder_paths_media_folder_id_fkey FOREIGN KEY (media_folder_id) REFERENCES public.media_folders(id) ON DELETE CASCADE;


--
-- Name: media_item_libraries media_item_libraries_content_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_item_libraries
    ADD CONSTRAINT media_item_libraries_content_id_fkey FOREIGN KEY (content_id) REFERENCES public.media_items(content_id) ON DELETE CASCADE;


--
-- Name: media_item_libraries media_item_libraries_media_folder_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.media_item_libraries
    ADD CONSTRAINT media_item_libraries_media_folder_id_fkey FOREIGN KEY (media_folder_id) REFERENCES public.media_folders(id) ON DELETE CASCADE;


--
-- Name: page_sections page_sections_library_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.page_sections
    ADD CONSTRAINT page_sections_library_id_fkey FOREIGN KEY (library_id) REFERENCES public.media_folders(id) ON DELETE CASCADE;


--
-- Name: playback_history_admin playback_history_admin_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.playback_history_admin
    ADD CONSTRAINT playback_history_admin_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: seasons seasons_series_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.seasons
    ADD CONSTRAINT seasons_series_id_fkey FOREIGN KEY (series_id) REFERENCES public.media_items(content_id) ON DELETE CASCADE;


--
-- Name: user_aggregates user_aggregates_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_aggregates
    ADD CONSTRAINT user_aggregates_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_downloads user_downloads_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_downloads
    ADD CONSTRAINT user_downloads_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_favorites user_favorites_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_favorites
    ADD CONSTRAINT user_favorites_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_personal_collection_items user_personal_collection_items_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_personal_collection_items
    ADD CONSTRAINT user_personal_collection_items_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_personal_collections user_personal_collections_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_personal_collections
    ADD CONSTRAINT user_personal_collections_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_playback_sessions user_playback_sessions_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_playback_sessions
    ADD CONSTRAINT user_playback_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_profile_allowed_libraries user_profile_allowed_libraries_library_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_profile_allowed_libraries
    ADD CONSTRAINT user_profile_allowed_libraries_library_id_fkey FOREIGN KEY (library_id) REFERENCES public.media_folders(id) ON DELETE CASCADE;


--
-- Name: user_profile_allowed_libraries user_profile_allowed_libraries_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_profile_allowed_libraries
    ADD CONSTRAINT user_profile_allowed_libraries_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_profiles user_profiles_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_profiles
    ADD CONSTRAINT user_profiles_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_ratings user_ratings_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_ratings
    ADD CONSTRAINT user_ratings_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_settings user_settings_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_settings
    ADD CONSTRAINT user_settings_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE ONLY public.user_device_settings
    ADD CONSTRAINT user_device_settings_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_subtitle_preferences user_subtitle_preferences_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_subtitle_preferences
    ADD CONSTRAINT user_subtitle_preferences_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_taste_clusters user_taste_clusters_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_taste_clusters
    ADD CONSTRAINT user_taste_clusters_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_taste_profiles user_taste_profiles_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_taste_profiles
    ADD CONSTRAINT user_taste_profiles_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_watch_history user_watch_history_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_watch_history
    ADD CONSTRAINT user_watch_history_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_watch_progress user_watch_progress_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_watch_progress
    ADD CONSTRAINT user_watch_progress_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- Name: user_watchlist user_watchlist_user_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.user_watchlist
    ADD CONSTRAINT user_watchlist_user_id_fkey FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE;


--
-- PostgreSQL database dump complete
--



-- Seed data
INSERT INTO public.server_settings (key, value) VALUES
  ('auth.access_token_expiry', '1h'),
  ('auth.refresh_token_expiry', '30d'),
  ('database.max_connections', '20'),
  ('s3.path_style', 'true'),
  ('s3.metadata_presign_expiry', '4h'),
  ('metadb.url', 'https://metadb.example.invalid'),
  ('playback.ffmpeg_path', '/usr/lib/jellyfin-ffmpeg/ffmpeg'),
  ('playback.transcode_dir', '/tmp/streamapp-transcode'),
  ('playback.hw_accel', 'auto'),
  ('playback.transcode_enabled', 'true'),
  ('playback.allow_hevc_encoding', 'false'),
  ('playback.transcode_ahead_segments', '30'),
  ('playback.segment_duration', '6'),
  ('scanner.schedule', '*/15 * * * *'),
  ('scanner.workers', '8'),
  ('scanner.file_removal_grace', '24h'),
  ('matcher.workers', '8'),
  ('matcher.batch_size', '500'),
  ('jellyfin_compat.public_url', 'http://127.0.0.1:8096'),
  ('jellyfin_compat.emulated_server_version', '10.12.0'),
  ('jellyfin_compat.server_name', 'StreamApp'),
  ('jellyfin_compat.session_ttl', '87600h'),
  ('jellyfin_compat.playback_session_ttl', '6h'),
  ('userdb.backend', 'postgres'),
  ('userdb.pool_max_open', '500'),
  ('userdb.idle_timeout', '12h'),
  ('userdb.litestream_sync', '1s'),
  ('userdb.stale_grace_seconds', '120')
ON CONFLICT (key) DO NOTHING;
INSERT INTO public.server_settings (key, value) VALUES ('playback.watched_threshold', '90')
ON CONFLICT (key) DO NOTHING;

-- Restore search_path after pg_dump's set_config('search_path', '', false) on line 15
SELECT pg_catalog.set_config('search_path', 'public', false);
