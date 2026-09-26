# Swagger UI assets

`swagger-ui-bundle.js` and `swagger-ui.css` are unmodified files from
`swagger-ui-dist` **5.32.15**, distributed under the included Apache 2.0
`LICENSE` and `NOTICE`. The unmodified `swagger-ui-bundle.js.LICENSE.txt`
contains the JavaScript bundle's dependency notices. Its URL serves these
notices together with the upstream license and notice, all embedded in the binary.

Source archive: <https://registry.npmjs.org/swagger-ui-dist/-/swagger-ui-dist-5.32.15.tgz>

Archive npm integrity:
`sha512-TSFER+rFQlf1nzk6WvKkMaHTxAPQ3eAAxigFThnxQedSREanfZgSbJFayZVs/ULnSbNdrJOb99vLD6xpb3R3eg==`

To update, select an exact `swagger-ui-dist` version, verify its archive against
the npm `dist.integrity` value, and replace the two assets, dependency notices, license and notice
from that archive. Update this record and verify the page, filtering,
authorization and interactive requests. Keep source maps and unrelated upstream
files out of the bundle. `index.html` and `init.js` are Silo's viewer setup.

The server embeds these files so viewing the documentation needs no CDN access.
The initializer disables the external validator, URL-based configuration and
authorization persistence. Interactive requests use the current server and its
normal authorization rules.
