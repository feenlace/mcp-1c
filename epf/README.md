# EPF connection

The connector provides configuration and metadata reads, static form metadata, SELECT queries, syntax validation, permitted-record counts, physical storage maps, and bounded event-log reads through the existing Community tools. Unsupported routes return a capability error. Local and dump tools retain their existing behavior.

The same EPF optionally detects the installed MCP_HTTPService core. A Pro build
with a valid verified grant can opt in to the fixed document identity/describe/get
READ adapter; Community and Advanced keep that route closed. Missing, old or
unusable core preserves FREE reads and gives an actionable hybrid refusal. This
adapter permits ordinary draft create/update with the existing typed scalar fields and optional whole-table replacement through preview/consent/transaction. Omitted tables remain unchanged; an explicit empty array clears the named table. The existing draft Date, metadata-qualified Number, directional deletion Mark and exact UNF-PAYMENT-AMOUNT-1 Amount intents reuse their finite read/prepare/commit contracts. List and unrelated special actions remain closed. The native adapter checks
actual core ownership, base, user and native session before and after reading.
The identity/read workflow also pins the paired worker; after re-pair, start a
new document read.
New starts use the conservative verified grant/request cutoff in native UTC
seconds. Native code does not verify license signatures; remote clock agreement
and other client profiles require qualification before use.

Pro can also opt in to the fixed `reconcile_read/1` identity/selection adapter
when its installed core advertises that separate marker. The ordinary reconcile
profile, CSV/XLSX, local owner/run and artifact contracts stay in the server.
One operation pins the paired worker/core and its original verified Pro cutoff;
a replacement grant cannot extend later reads in that operation. Native RC forwards
the same optional cutoff into the package engine. Legacy HTTP/polling callers
retain their default behavior. A missing marker refuses before native dispatch
and leaves FREE reads available. `get_run` remains a paid read. The Pro-compiled `workflow_status` can observe
a data-free status of an owned EPF plan, batch, stored run or report capture after
expiry; it does not execute or recover work. Legacy records without native
admission proof are unavailable. Masking is enforced. Report capture status does not execute or recover work.

Pro can separately opt in to `quality_read/1` through the installed core.
Ordinary `code_read.quality` retains profile/history/revalidation and both selected
rules/locations; it has no file input. One operation pins worker/core and the
original verified Pro cutoff. Each new catalog/location selection checks that
cutoff; final schema/rights/identity integrity checks preserve an already started
result. Missing/old core refuses before handout; FREE remains available. Masking
keeps its existing pre-handler refusal, and full history remains a paid read.

The form calls the local bridge from the 1C client: desktop uses short synchronous
HTTP requests (timeout2seconds); web uses asynchronous HTTP. Business reads still
execute on the 1C server under the current native user, with unchanged SafeMode
and business rights. `127.0.0.1` means the desktop/browser machine running MCP,
which can differ from the 1C server. This profile requires MCP on that client
machine. It installs no CFE or native component and does not bypass SafeMode.

The web API requires platform8.3.21.1302 or later; the currently available native
test platform is8.3.27. The desktop branch excludes modern web async syntax and
uses the older client HTTP API, but actual native14 support is NOT VERIFIED.
This common artifact does not establish support for web14. The existing product
CFE compatibility/minimum8.3.14 is unchanged. See the local validation report for
which client and restricted-user profiles have actually passed.

## Run

Use a **new dedicated path** in an existing private local directory. MCP creates
its private issuance file and writes a fresh random 64-hex code at listener
startup and authenticated Stop. Read the second line (the first is
`MCP-EPF-ONE-TIME-CODE/1`) into the existing code field. Each code expires after
five minutes and permits exactly one successful connection. Malformed requests
do not consume it. An established session keeps working after that deadline.

The file is server-managed, not an input-token file. Existing plain-token or
unrecognized files are refused unchanged: keep the old file and configure a new
dedicated path. Later starts refresh only a recognized managed file. Two live
servers cannot share it. Do not edit, replace, move or change its permissions
while MCP runs. Unix mode is0600; Windows uses the current user as owner with a
protected current-user-only DACL established before the secret is written.
Run MCP under that same user. Shared/inherited ACLs, symlinks/reparse points,
replacement and concurrent issuers are refused. Keep the directory and file
outside the repository; never put codes or session credentials in CLI arguments,
logs, screenshots or bug reports. No persistent reconnect credential is created.

If startup or Stop reports a file error, fix the private path/permissions and
close any other issuer, then restart MCP and read the fresh code. Failed Stop
reissue still stops the session; it never restores the old code. An expired code
or lost successful pair reply requires an explicit MCP restart and a fresh code;
no automatic operation replay occurs. A torn/unrecognized managed file is refused:
preserve it and select a new dedicated path.

Configure the ordinary MCP client to launch this Community binary with:

```
mcp-1c --epf-listen 127.0.0.1:9090 --epf-token-file /absolute/private/pairing-token
```

These flags replace the HTTP base connection; do not combine them with HTTP
base/user/password configuration. The listener requires literal127.0.0.1 and
an exact Host. Desktop requests omit Origin. Browser requests are refused unless
you also configure the single actual 1C publication origin, for example:

```
mcp-1c --epf-listen 127.0.0.1:9090 --epf-token-file /absolute/private/pairing-token --epf-web-origin http://127.0.0.1:59342
```

Replace the example with the publication origin visible in the browser. This
trusts the whole origin, including other paths hosted there. HTTPS origins are
allowed; HTTP origins must be loopback. Paths, credentials, wildcards, lists and
`null` are refused. Only the configured exact Origin receives route-scoped CORS
preflight permission; every real request still needs its bearer token and normal
session/identity/admission checks. Browser network or mixed-content policy may
still prevent a remote HTTPS publication from reaching the local HTTP bridge;
that topology is not verified. There is no insecure browser-policy fallback.

## Named connections

The unnamed flags above keep their single default connection. Alternatively name
each EPF flag with the same lowercase ASCII name (letters, digits, `_`, `-`;
start with a letter, at most64 characters). Community accepts one named
connection per process and keeps its existing single-base tool arguments.

Advanced/Pro use the existing multibase tool contract. Repeat the named flags
for independent native connections, for example:

```
mcp-1c-advanced \
  --epf-listen source=127.0.0.1:9090 --epf-token-file source=/absolute/private/source-code \
  --epf-web-origin source=http://127.0.0.1:59342 \
  --epf-listen target=127.0.0.1:9092 --epf-token-file target=/absolute/private/target-code \
  --epf-web-origin target=http://127.0.0.1:59344 --default-base source
```

Open the same EPF in each intended base under its intended native user. Enter
that base's listener port and its own issued code. `list_bases` exposes
`epf://source` and `epf://target`; pass the matching `base` tool argument. Each
base has its own code, session, native identity, task queue and original result
collection. Stop rotates only that connection's code. A native copy must have a
distinct real connection identity; two names alone do not make two infobases.

Named EPF connections can coexist with ordinary named HTTP/polling bases using
the existing flags and guards. Do not repeat an EPF name in `--base` or `--auth`:
its connection is defined by its EPF flags and authenticated by its private code.
`--default-base`, `--read-only NAME` and `--skip NAME` keep their ordinary
registry meaning. Skipping a named base also leaves its EPF listener and code
file unopened. Use distinct literal loopback ports and dedicated private files;
duplicate names/ports/files and missing/mixed named/unnamed flags are refused.
Old plain input-token files remain refused unchanged. Named routing alone
does not establish Graph acceptance; each Graph profile requires qualification.

Build the external processing from `source/MCPConnector.xml` using an existing
owned synthetic Designer session and the established command structure:

```
DESIGNER /F <owned-build-infobase> /WA- /DisableStartupDialogs /DisableStartupMessages \
  /LoadExternalDataProcessorOrReportFromFiles <absolute-source>/MCPConnector.xml <new-owned-output>/MCPConnector.epf \
  /Out <private-build-log> /DumpResult <private-result>
```

`/Out` is the log, not the EPF artifact. Check the process return code and native
result/log and perform native readback. An existing source template alone does
not establish successful compilation or runtime acceptance. Do not build against
user/production bases or change trust/licensing settings to make it work.

Open the resulting file in 1C under the intended ordinary native user. Enter the
configured port and fresh five-minute code, then choose **Подключить**. The form must stay
open. **Остановить** stops new work and releases the pairing; already started
native work may still complete. Read the newly issued file code before connecting
again. After a server restart, stop and pair with its new code; all prior codes
and sessions are invalid.

For an update, keep the old EPF and the original operation/receipt references.
Stop, open the new verified EPF and pair with a fresh code. Stop does not cancel
work already running. Never automatically retry an UNKNOWN result; inspect the
original status or receipt using the retained references. Roll back only after
compatibility is verified on a copy. Do not reset or delete receipt metadata.
The installed update and rollback flow is not yet verified.
Changing the native principal/base identity refuses further work. The installed core stores a successful EPF ordinary/special mutation receipt atomically in the existing MCP_DocumentReceipts register. Its existing Outcome string gains a versioned EPF admission/owner envelope. Legacy plain outcomes remain readable. The isolated `MCP_CodeExecution` common module is not a register. Back up and validate payload and extension compatibility on a copy before updating; RIB impact and the upgrade remain unverified.

## Protocol boundaries

The server classifies a finite set of capabilities; it rechecks the admission
callback at handout. No caller BSL, arbitrary method/URL, CFE identity or paid
license is accepted from the worker. Pairing authenticates the operator-selected
worker and binds its reported native identity; it is not remote attestation of
1C or authority for hybrid writes. Those require the fixed installed CFE contract, actual native owner/context
and verified SDK execution admission. Receipt observation separately requires its
stored original EPF admission; it never grants a new write.

One session has at most16 outstanding tasks. Limits are finite and vary by
route: the ordinary default is 64KiB for a task body and 2MiB for a result
envelope, while selected capabilities use their own bounds. Do not treat the
default as a universal limit.
Poll returns immediately with a task or204; the form schedules a single next poll one second after the preceding cycle finishes.
The five-minute lifetime applies to the one-use pairing code, not to the connected session.
Stop or server shutdown ends the session; an expired paid entitlement does not itself end supported FREE reads.
Execution admission is at most30seconds; result collection extends one minute
beyond it. Neither timeout nor closing the form proves native cancellation.
There is no automatic task replay. A bound terminal result can be collected after
execution expiry or session stop while its original collection window remains;
result acceptance does not recheck paid entitlement. If the original caller times
out or cancels after handout, `UnknownOutcome` is an opaque process-local ticket;
the owning adapter can use `Bridge.Observe` to read the retained terminal result
until the original collection deadline. A new session cannot replace it. This is
not a new public status endpoint, durable restart recovery or replay authority.
Cancellation before handout removes the task and is checked again at handout; no
native work is dispatched for it. Native allocation and long
query execution are not bounded by the HTTP envelope limit.

No logs contain pairing/session tokens or native response bodies. Source/build
checks and the synthetic Go worker are not desktop/web/native acceptance.

The EPF transport opts into `onec.Client` request-context timeout handling. The
configured timeout still applies (and the public HTTPClient.Timeout value remains
unchanged); only an opted-in request uses a local client clone without the lossy
outer net/http timeout wrapper. This preserves `errors.As` access to the original
owner ticket. Legacy HTTP transports keep their existing timeout path. A transport
wrapper around EPF must preserve its `UseContextTimeout` opt-in.

The adapter's `/version` response identifies `connector_version` and protocol1;
it does not report an installed CFE version. With the server-configured EPF
transport, `environment_passport` publishes `native_connector_version` and
`native_connector_protocol` with source `live_connector`; `extension_version`
remains absent. Missing/unsupported adapter metadata is reported as unavailable
or unsupported without blocking other tools. HTTP/polling keep their existing
CFE version semantics. Neither version namespace authorizes capabilities or
paid operations; those remain separate server admission/native contracts.

Structural metadata uses a finite kind/name route and the current native user. Form
reads inspect metadata only, without opening or initializing the business form.
Only the static form name/title is available through this route; its response
explains that elements, commands and handlers require the existing `--dump` source.
Missing objects/forms return not-found, ambiguous subsystem names return the
existing candidate list, and partial subsystem collection retains safe warnings.
An unavailable metadata API is an unavailable source, not proof of a rights denial.

`validate_query` uses the native query schema parser without executing a query.
It retains parser-position diagnostics without the outer BSL module wrapper.
`count_records` admits Catalog/Document metadata, exact civil [start,end) dates
and typed Catalog references only. Its fixed generated SELECT uses current
read/RLS rights and parameters; it does not load rows or accept query expressions.
The existing client requires nonnegative int64 and exact applied-filter echoes.

`get_storage_structure` reads the native storage API for one selected object;
API errors or a missing map for a stored object are unavailable outcomes. An
actual empty native result is retained only for objects without own storage.
`get_event_log` checks the native Administration right before reading/filtering.
Unknown, null, duplicate or unapplied filters are refused; a request typo must
not become an unfiltered log. Native read and response limits remain bounded.
Event records can contain sensitive operational data; only the authorized
caller receives them, and transport/connector logs do not record their bodies.

The Pro-compiled `document_receipt` tool reads only a preexisting EPF-admitted
ordinary/special mutation receipt after expiry or restart. Retain the exact `base` and `business`
strings delivered in the original consent preview. Stop and pair again after a
server restart, then pass those same bytes: no old session token is needed. A
missing, unbound or foreign receipt is not evidence of rollback and never starts
a write or recovery. Current native user/base/core and RLS still apply. This
pilot requires explicit masking OFF: protected mode refuses both the mutation
preview and receipt data before dispatch. Explicit masking OFF is required for
these pilot actions; renewal does not add them to the protected-read allowlist.
Protection is never disabled automatically. Native metadata is
not cryptographic license attestation. Historical HTTP/poll receipts do not gain
EPF admission merely by being present.


Advanced and Professional servers can explicitly enable `code_execute` action
`query_package`. This EPF route requires an updated installed MCP_HTTPService
core with the fixed EPF package export; reconnect after the administrator updates
it. Without that core only this paid capability is unavailable; FREE reads stay
usable. The native package uses ordinary user rights/RLS, checks the actual empty
receipt scope and the admitted execution cutoff, and closes its own temporary
manager. Task/result JSON strings retain the exact native input/output bytes.
The common Community binary does not enable this route. Protected-mode output
uses the existing query-package masking policy; protection is never disabled
automatically.

The same Advanced/Professional opt-in now supports ordinary MCP `code_read`
action `object_diff` with native `object_diff/1` capability negotiation. Update
MCP_HTTPService and reconnect if the comparison capability is unavailable;
FREE reads and an older core's supported package reads remain usable. This
compares two sequential observations under the connected native user/RLS, with
its execution cutoff checked before the first data read. It does not establish
unrestricted coverage or snapshot consistency. The separate BSP form command
requiring a supplied principal probe is unavailable through EPF; use its existing
supported authenticated HTTP route. Community does not enable comparison.

Advanced/Professional opt-in also supports ordinary `system.job_diagnose`
through the fixed `JOB-DIAG-1` core export. Update MCP_HTTPService and reconnect
if this capability is missing; supported FREE/package/comparison reads continue.
The native adapter retains current-user job rights and empty receipt scope,
with the live signed Advanced cutoff rechecked before handout and native job
reads. Event-log collection remains a separate FREE read with its own rights.
No job control, retry, incident or UI principal flow is enabled by this route.

Professional may separately opt into the read-only stock-job hybrid with
`job_control_read/1` and `job_retry_read/2`. It requires the installed native
core and the original verified Pro grant, session, actor and base. It forwards
only `/job-control-read`, `/job-retry-identity` and `/job-retry-read`; existing
`system.job_control` supports control `reconcile` and retry `preview/get/reconcile`.
Retry preview writes a local immutable plan; paid retry reconcile may update its
local observation and resource fence. Neither route starts or cancels a native
job through these read-only markers. The owning Pro server may additionally
opt into `job_control/1` and `job_retry/2` for the existing selected cancel and
retry start/cancel routes. These require the original Pro cutoff and session/core,
fixed stock profile, fresh selection and explicit live consent. Native execution
clamps the request to that cutoff and serializes it to the retry worker; the
worker independently rechecks its deadline/actor/readiness before stock entry.
Unknown delivery remains fenced and must reconcile the same operation, never
automatically replay. Old or missing cores keep FREE and their other negotiated
capabilities; update the existing core in place without changing rights or
security settings.

Pro `workflow_status(kind=job_retry)` takes exactly `base`, `kind`, `id` and
observes only the last validated local stored state, including after expiry.
It authenticates the current native owner using technical document identity;
no paid job lookup, fresh progress, business payload, fence write/release or
recovery occurs. UNKNOWN remains UNKNOWN until a renewed paid reconcile.
Completed counts describe a stored terminal job observation, not index correctness.
The native logical base/principal owns the record across configured aliases and
transports; a pair advertisement or guessed row ID does not grant access.


Professional can separately opt into `system.incident_timeline` through the
fixed `INCIDENT-TIMELINE-1` core adapter. Update MCP_HTTPService and reconnect
if unavailable; supported FREE/JD/package reads remain usable. The existing
job, clock, session, selected storage and explicit local TJ contract is retained,
including individual rights/failure/not-requested statuses and caller-declared
offsets. Native context reads require current Pro admission and their cutoff;
FREE eventlog keeps its separate authority. A TJ file requires its explicit
path, hash and containment checks; no native provenance or correlation is inferred.


Professional separately opts into ordinary `read_exchange_queue` with the optional
`read_exchange_queue: queue_observation/1` pair marker. It requires the current
installed MCP_HTTPService queue export and a fresh pair. Both canonical profiles
retain native schema, rights and RLS checks. Describe and read are pinned to the
same pair/worker/core, principal, base, schema and selected node. Exact request
and result use string envelopes. The verified live Pro grant is checked at
enqueue and handout; the native cutoff is checked before each queue data read.
Cancellation after handout retains an opaque unknown outcome, without replay.
Coverage is only a current-principal observation, including access checks for
the extra truncation row; empty or untruncated output never establishes global
completeness. Older or absent cores refuse the queue route while keeping their
supported FREE reads. No registration/message counters are changed.

Advanced and Pro can opt in to the existing `code_execute.code` endpoint when
the real installed core advertises `code_execute/1`. The existing Go sandbox,
10KiB code limit, default read_only mode, explicit full-mode confirm=true,
audit, masking and connection read-only policy still apply. No new elicitation
prompt is added. One request pins native actor/base/session/core; enqueue,
handout and native admission check the original verified Advanced cutoff. An
already admitted result survives paid expiry. A native timeout or lost result
does not prove rollback: inspect the actual result before repeating code.
There is no atomic receipt or automatic replay for arbitrary BSL. Missing or
old native core refuses actionably and preserves FREE reads. Native platform
cannot forcibly stop arbitrary BSL at the public request timeout.


`graph_transfer` in Pro uses the same finite native Graph profiles over two
independent named EPF connections: GRAPH-COUNTDOCS-1 and the already maintained
UNF-COUNTERPARTY-1. Each retains its existing schema, native rights, source
versions, target UUID allocation, reference mapping, consent, transaction and
UNKNOWN/recovery rules. UNF still requires its separately qualified stock
installation; EPF support does not qualify a configuration. Update both the
installed MCP_HTTPService core and EPF, then reconnect; an older core refuses
Graph while FREE remains usable. Every new native call uses the original
per-base grant cutoff and worker pin.

Before consent, save the exact `document_receipt` arguments from the Graph
preview. This existing read-only action can observe only a matching original
EPF owner/admission receipt after expiry or restart. It never starts or replays
Graph, creates a local Store, or treats an absent receipt as permission to retry.
The same existing receipt register/transaction stores the versioned owner
admission envelope; legacy plain Graph outcomes are not upgraded into EPF
owner proof. Graph `get` and explicit `recover` remain paid; recover may commit
if the original receipt is absent and the original source/consent still match.

## Local candidate staging

For local inspection only, `make local-epf-rc RC_OUTPUT=/absolute/new-directory
RC_PROOF=/absolute/proof-directory` stages the specifically pinned native Graph
candidate and its existing macOS ARM64 servers. The proof directory must contain
the matching public build/pack packets, native readback and artifacts. The
command refuses missing or altered inputs and an existing output directory; it
does not build the EPF. Its manifest records each edition's source binding and
limits. This historical candidate is not the current Advanced/Pro source and
contains a local replacement; its Advanced/Pro binaries use a synthetic test issuer. It is not publishable or a release, installation
or upgrade acceptance. A future release needs its own verified artifacts and
explicit release authorization.
