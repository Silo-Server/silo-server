-- +goose Up
-- An advisory age is a recommendation aimed at a parent ("Common Sense Media
-- suggests 13+"), not a rating body's certification. It is display only:
-- nothing here feeds content_rating_age, ApplyContentRatingCeiling, or any
-- other parental control.
--
-- Keeping it out of enforcement is a correctness requirement, not caution.
-- The provider that supplies it (the MDBList plugin) spends one request per
-- title against a 1000/day free tier, so on a real library it enriches an
-- arbitrary slice and goes quiet until the next day. That is fine when the
-- consequence is "some titles show an advisory age and others do not", and
-- unacceptable for a ceiling: titles would tighten or not for reasons no admin
-- can see, predict, or reproduce, and the set would change daily.
--
-- No backfill: no advisory data exists anywhere yet.
--
-- No index: nothing filters or sorts on either column while this is display
-- only. Enforcement, if it is ever wanted, would add its own index alongside
-- the ceiling predicate it changes.
--
-- Not propagated to episode_catalog_entries either, for the same reason
-- content_rating_age is: that copy exists so the access filter can apply a
-- ceiling against the "ece" alias. Display-only data has no such filter, and
-- an episode's badge is rendered from its series' detail document.
ALTER TABLE public.media_items
    ADD COLUMN IF NOT EXISTS advisory_age smallint;

-- The source is stored, not inferred, so the UI can attribute the number
-- ("Common Sense") rather than presenting an anonymous age. The host
-- allow-lists the values it accepts before they reach this column.
ALTER TABLE public.media_items
    ADD COLUMN IF NOT EXISTS advisory_source text;

-- +goose Down
ALTER TABLE public.media_items
    DROP COLUMN IF EXISTS advisory_source;

ALTER TABLE public.media_items
    DROP COLUMN IF EXISTS advisory_age;
