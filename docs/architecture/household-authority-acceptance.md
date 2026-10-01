# Household authority acceptance

Run the nine-case packet with `SILO_SCENARIO_REQUIRED=1 go test -count=1
-run '^TestRequiredHouseholdAuthorityAcceptance$' ./internal/scenariocatalog/executor`.
Set `SILO_SCENARIO_DATABASE_URL` to an exclusively owned disposable PostgreSQL
instance with project extensions. Missing configuration, foreign fixtures/API keys,
and occupied playback state refuse before environment construction. This runner
reseeds application fixtures; never use a shared or deployed database.

Eight frozen household-session cases retain their actual account/profile authority
and original empty-playback fixture. V2 uses the unpaged `items` envelope with no
`page` field. No populated session filtering, loader completeness or playback
lifecycle claim follows from these originals.

The ninth case retains the original `STANDARD` quality request and its `1080p`
response. V2 submits the accepted canonical `1080p` value. This proves the original
alias normalization and equivalent canonical update, not v2 alias acceptance or
other quality aliases described but not exercised by that original request.

Every transport reseeds before and after execution and compares all columns of
26 tables. Reads have no effect exemptions. Quality updates change only the target
profile ceiling and its application-clock-bounded update timestamp, plus exactly
one increment of the owning account's access-policy and administrator revisions.
All other accounts, profiles, settings, credentials, devices, invitations,
onboarding, library restrictions and playback-session rows remain unchanged.
The embedded packet locks complete originals and explicit v2 expectations.

For a recorded collection-expectation correction only,
`SILO_HOUSEHOLD_READBACK_FOLLOWUP=1` executes exactly the four successful v2
household reads with strict four-result/eight-snapshot checks. It is not standalone
nine-case acceptance: combine its report with the unchanged fourteen successful
exchanges from the full run, retaining the initial failures and both reports.
The normal invocation always requires all eighteen exchanges and 36 snapshots.
