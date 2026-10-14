# Third-Party Notices

This repository vendors a small amount of third-party and generated asset
material needed for Silo server builds.

## Jellyfin Web

Silo does not bundle Jellyfin Web in this repository or in the default runtime
image. Administrators can explicitly install Jellyfin Web as a separate
compatibility component with `silo compat-web install` or the admin settings UI.
The installer records the upstream source URL, tag, commit SHA, checksum,
license, and source/provenance metadata beside the installed assets.

## Collection Template Posters

`web/public/images/collection-templates/` contains Silo-generated collection
poster artwork. The artwork is intended to use generic original scenes and
avoid copyrighted movie/show posters, recognizable actors, franchise
characters, provider logos, and readable in-image third-party branding.
