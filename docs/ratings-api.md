# Ratings API

Silo ratings have two deliberately separate uses:

- A personal rating belongs to one account profile and remains an input to that profile's recommendation taste.
- A community rating surface shows how profiles on the same Silo server rated an item. Its average and reactions are display-only and never affect recommendations.

All documented data and mutation routes require an authenticated, selected profile. Existing personal-rating routes and behavior are unchanged.

## Capability discovery

`GET /api/v1/ratings/capabilities`

```json
{
  "community_ratings": true,
  "community_rating_reactions": true
}
```

Clients should check this endpoint before presenting the optional community surface. The web client supports it on movie and main series detail pages. Apple, Android, and Jellyfin-compatible clients do not currently expose this Silo-specific surface.

## List community ratings

`GET /api/v1/ratings/{item_id}/community?limit=100`

Every explicit profile rating is eligible for inclusion, whether or not that profile has watch progress. The response returns up to the requested card-list limit (maximum 100); the average and vote count cover every explicit rating for the item.

```json
{
  "average_rating": 4.5,
  "vote_count": 2,
  "ratings": [
    {
      "key": "opaque-rating-key",
      "display_name": "S*******",
      "avatar_url": "/profile-avatars/avatar-1.svg",
      "rating": 5,
      "rated_at": "2026-08-29T08:00:00Z",
      "up_count": 3,
      "down_count": 1,
      "viewer_reaction": "up",
      "is_viewer": false
    }
  ]
}
```

`average_rating` is `null` when there are no votes. The existing `user_ratings` primary key keeps one rating per account/profile/item: changing the stars updates that row and clearing the personal rating removes its community card. Profile and account IDs are never returned. `display_name` contains the first Unicode character followed by one `*` for every hidden character. `is_viewer` lets clients distinguish the selected profile when protected display names happen to match. Avatar URLs are resolved from the profile's current avatar when the response is created, so later profile changes are reflected on refresh. Uploaded avatars use a short-lived opaque proxy URL; private storage keys are not sent to other profiles.

## React to a rating

Set or change the selected profile's one reaction to any rating, including its own:

`PUT /api/v1/ratings/{item_id}/community/{rating_key}/reaction`

```json
{ "reaction": "up" }
```

`reaction` is `up` or `down`. Sending the other value changes the existing reaction rather than adding a second one. Remove the selected profile's reaction, including when the same button is selected again, with:

`DELETE /api/v1/ratings/{item_id}/community/{rating_key}/reaction`

Both successful mutations return `204 No Content`. Ratings and reactions are stored in PostgreSQL and survive normal server and container restarts.
