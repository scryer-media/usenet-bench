# Published SABnzbd advisory coverage

This mapping was reviewed against SABnzbd's published repository security
advisories on 2026-09-08. It distinguishes portable hostile-download primitives
from vulnerabilities in SABnzbd's own Python, CherryPy, configuration and state
model. A product-specific advisory is not represented as a generic fixture when
there is no honest client-neutral input contract for it.

| Advisory | Primitive | Suite disposition |
| --- | --- | --- |
| [CVE-2021-29488 / GHSA-jwj3-wrvf-v3rp](https://github.com/sabnzbd/sabnzbd/security/advisories/GHSA-jwj3-wrvf-v3rp) | PAR2 filename moves a matching downloaded file through `../` | Exact generable regression: `historical-regression-sab-cve-2021-29488-par2-parent-escape`. A Weaver 0.11.3 run reached placement and encoded the parent component, keeping the target in-root. |
| [GHSA-75g3-96fr-7p2r](https://github.com/sabnzbd/sabnzbd/security/advisories/GHSA-75g3-96fr-7p2r) | An unpacked PAR2 filename escapes its job and can poison trusted cross-job state | Exact input regression: `historical-regression-sab-ghsa-75g3-unpacked-par2-parent-escape`. Weaver 0.11.3 extracted the PAR2 as an inert output, so the historical placement sink was unreachable in that run. |
| [GHSA-mjwj-v5mr-cmcg](https://github.com/sabnzbd/sabnzbd/security/advisories/GHSA-mjwj-v5mr-cmcg) | An in-tree symlink combined with `pivot/..` defeats lexical normalization before PAR2 rename | Exact input regression: `historical-regression-sab-ghsa-mjwj-symlink-dotdot-par2-escape`. Weaver 0.11.3 produced a regular one-byte `pivot` file and left the extracted PAR2 inert, so neither prerequisite for the combined sink was present. |
| [GHSA-rgqj-28c2-gxwp](https://github.com/sabnzbd/sabnzbd/security/advisories/GHSA-rgqj-28c2-gxwp) | HTTP route/parameter authorization confusion, later chained through script settings | Not a download fixture. Weaver uses route-level scope resolution and structured GraphQL mutations; test in the Weaver HTTP authorization suite. Option-like and shell-bearing content remains covered by the NZB/path and command-canary cases. |
| [GHSA-q326-jpxx-jmjc](https://github.com/sabnzbd/sabnzbd/security/advisories/GHSA-q326-jpxx-jmjc) | CherryPy dispatch reaches a Python decorator's exposed `__wrapped__` handler | Not applicable to Weaver's Rust/Axum dispatch model and not representable as an NZB/NNTP fixture. |
| [GHSA-xrfq-jhgh-wqch](https://github.com/sabnzbd/sabnzbd/security/advisories/GHSA-xrfq-jhgh-wqch) | Logout endpoint issues an authenticated CherryPy session | Not a download fixture. Test session issuance and invalidation in the Weaver HTTP authorization suite. |
| [CVE-2026-62965 / GHSA-hxwh-mmrg-p8f5](https://github.com/sabnzbd/sabnzbd/security/advisories/GHSA-hxwh-mmrg-p8f5) | Authenticated orphan-management API joins an absolute path and recursively deletes it | Not a download fixture. Test each Weaver filesystem-management mutation with absolute, parent, symlink and reparse-point paths. |
| [CVE-2023-34237 / GHSA-hhgh-xgh3-985r](https://github.com/sabnzbd/sabnzbd/security/advisories/GHSA-hhgh-xgh3-985r) | Notification-script parameter construction permits command execution | Not directly reproducible from downloaded bytes. Weaver's post-processing runner and admin-only settings require a dedicated configured-script harness; content-side metacharacters are covered by inert command-canary cases. |
| [CVE-2020-13124 / GHSA-9x87-96gg-33w2](https://github.com/sabnzbd/sabnzbd/security/advisories/GHSA-9x87-96gg-33w2) | Configuration fields reach shell commands without safe argument boundaries | Not a download fixture. Weaver configuration and post-processing runner tests must verify direct process arguments and reject unsafe executable/path configuration. |

The historical cases are regressions for exploit primitives, not claims that the
SABnzbd implementation or exploit chain exists in another client. They target
the fixed `/scratch/escape.canary`; any change is a review-required potential
finding in supervised runs. Each generated manifest also names the runtime
prerequisite required to reach its historical sink; extraction alone is not
reported as PAR2 placement coverage.

## Weaver 0.11.3 supervised API probes

A fresh, isolated 0.11.3 instance was configured with login-required access and
then probed with SAB-advisory-inspired requests. The authenticated control
returned 200. A valid browser cookie paired with an invalid explicit API key
returned 401 rather than falling back to the cookie. Unauthenticated GraphQL
mode-confusion and JSON-RPC configuration attempts returned 401, a POST to
`/graphql/__wrapped__` returned 405, and logout expired both the JWT and local
session cookies. No potential finding was observed.

These probes cover the attempted routes and credentials only. The orphan-delete
advisory has no corresponding Weaver endpoint, and intended administrator
configuration of post-processing executables is a separate trust boundary.
Neither absence of an analogous route nor these five responses proves the whole
HTTP surface secure.
