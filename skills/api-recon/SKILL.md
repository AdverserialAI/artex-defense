---
name: api-recon
description: Discover a website's API endpoints and parameters.
---

# API Recon: frontend endpoint reconnaissance

Within authorized scope, discover backend API paths/methods/parameters/responses, frontend routes, and UI triggers such as tabs, dialogs, and table actions as completely as possible.

## Boundaries and prohibitions

This skill performs API/parameter reconnaissance only, not vulnerability discovery or exploitation.

| Area | Allowed | Prohibited |
| --- | --- | --- |
| Goal | Enumerate paths, methods, parameters, routes, UI triggers | SQLi/XSS/access-control attacks, brute force, vulnerability fuzzing, attack request modification, destruction |
| Authentication | Hook and stub/mock client-side login gates | Asking for/guessing credentials or submitting real login forms |
| Runtime | Hook APIs without credentials and use mock responses to render the post-login SPA shell | Workflows requiring a genuine backend session |

### Credential-free dynamic analysis: Phase 3 default

1. Intercept/stub login, permission, menu, and other bootstrap APIs using `preload.js` / `runtime_harvest.js`.
2. Return structurally correct mock bodies with successful business codes; data may be empty.
3. Render post-login pages even with no backend or HTTP 401, exposing further XHR/fetch/WebSocket calls.
4. Empty data/tables and placeholder UI are expected. Do not switch to real login or vulnerability testing to fill them.

Use mocks to expand route/component rendering and record outbound requests. The important question is which endpoints the frontend attempts to call, not the backend's real data.

### Workflow prohibitions

| Prohibited | Required alternative |
| --- | --- |
| grep/curl/Read of the main `index-*.js` entry for API extraction before Phase 1 completes | Run `OUTDIR/harvest_static.py` |
| Writing `extract_apis.py` or another replacement extraction script | Modify and rerun `OUTDIR/harvest_static.py` |
| Repeating the same failed grep/command at least twice | Inspect tool_logs, modify harvest, or consult reference |
| Skipping gates A/B and running original `scripts/` as final tools | Copy to OUTDIR and adapt to the target |
| Real credentials, OTP, OAuth authentication | Stub/mock |
| Skipping stubs to obtain real data through access-control/injection testing | Record outbound requests only |
| Irreversible deletion, sensitive-data exports, or bulk writes | Avoid these even during coverage clicking |
| Claiming complete pages/APIs without runtime and dynamic enumeration | Meet completion criteria or report limitations |
| Claiming complete parameters without trigger matrix and diff | Phase 3b matrix and Phase 5 diff |
| Inferring required/optional from one runtime sample | Multiple-sample diff or validation/error evidence |

## Two layers and runtime modes

| Layer | Output | Limits |
| --- | --- | --- |
| Static JS bundles | Endpoint paths, draft routes, candidate payload fields | No reliable HTTP method; parameters need Phase 1b; dynamic URL construction can be missed |
| Runtime | Methods, bodies, responses, dynamic URLs, WS/SSE, multi-sample parameter evidence | Pages must actually render; one sample cannot establish required/optional fields |

| Mode | Engine | Purpose |
| --- | --- | --- |
| `depth` | Puppeteer `runtime_harvest.js` | Reproducible batch API/method/parameter/response/WS/SSE collection |
| `coverage` | Browser + `preload.js` | Deeper functional coverage by clicking tabs/dialogs/tables |
| `both` | Depth, then coverage | Most complete, with the highest time cost |

Parameter methodology has no universal script: paths use harvest/regex; parameters require anchored context windows, UI binding chains, multi-sample diffs, and error-based inference. See [reference.md](reference.md), section J.

## Completion criteria

Claim completion only when all applicable items are satisfied:

- [ ] Static harvest produced `api_static.txt`, `routes.txt`, and `js/`.
- [ ] At least depth or coverage ran; coverage/both require active hooks and the dynamic enumeration loop.
- [ ] Business paths render without returning to `/login`; account for hash routing.
- [ ] Coverage/both completed the parameter trigger matrix and `param_samples.json`; Phase 5 produced `params_merged.json`.
- [ ] If modules were blank, Phase 4 restored permissions and Phase 3 was rerun until module-level APIs appeared, not just locale/bootstrap calls.
- [ ] All Phase 5 outputs exist and `insert_assets` registered every discovered service and API endpoint. Do not omit discovered assets.

## Scripts and gates

`scripts/` contains reference templates. Never run originals unchanged and treat their output as final.

Read → adapt → save in OUTDIR (for example `recon/`) → document changes in `CHANGES.md`. Rewrite target-specific logic when needed while reusing the structure.

| Gate | When | Copy into OUTDIR | Typical adaptations |
| --- | --- | --- | --- |
| A: static | After Phase 0, before the first harvest/spider | `harvest_static.py` / `spider_mpa.py` | Default regex works for most sites; adjust only for mismatched manifests/dialects, endpoint regex, webpack/Vite publicPath, MPA excludes/cookies |
| B: runtime | After Phase 2, before depth/coverage | `runtime_harvest.js` / `preload.js` + `config.json` | Storage/cookie keys, successful neutralize values, stubs, login regex, API prefixes, hash/history routing |

Mandatory SPA order: phase numbering takes precedence over generic explore-before-script advice.

- After Phase 0, the next Bash action is `python3 OUTDIR/harvest_static.py <URL> OUTDIR`; do not curl/grep/Read the main entry bundle, usually over 500KB.
- At gate A, copy, optionally adjust, then run immediately; do not manually extract APIs first.
- Before Phase 1 completes, validate with `wc -l`; fix harvest for 404s and retry. Do not write a substitute extractor or repeatedly grep undownloaded URLs.
- From Phase 1b onward, grep only `OUTDIR/js/*.js`; the main bundle is not a replacement for harvest.
- For MPA, the next Bash after Phase 0 is `python3 OUTDIR/spider_mpa.py ...`.

## Tool and output limits

| Item | Rule |
| --- | --- |
| Large files | Never Read/grep `index-*.js` larger than 100KB into context; batch-process with OUTDIR scripts |
| grep output | Limit with `\| head -20` or `-m 5`; retain path summaries, not bundle excerpts |
| Validation | Use `wc -l` and `ls \| wc -l`; do not Read an entire directory |
| Initial regex probe | Optional, at most once, against HTML or a chunk no larger than 50KB; formal static results come from harvest |
| Reference | Use [reference.md](reference.md) for recipes/templates/troubleshooting rather than copying it inline |

## Execution roadmap

```text
Phase 0: classify + OUTDIR
  → Gate A → Phase 1: harvest immediately
  → Phase 1b: parameter reverse engineering
  → Phase 2: three authentication gates → config.json
  → Gate B → Phase 3: runtime + parameter matrix
  → Phase 4: permissions, if needed → rerun Phase 3
  → Phase 5: merge/report + batch-register all services/endpoints with insert_assets
```

Finish each item before entering the next phase.

## Phase 0 — Classification

Fetch entry HTML and create OUTDIR. Do not modify the skill's `scripts/` originals.

- SPA: shell HTML, `<div id=app>`, and chunks → Phases 1–5.
- MPA: SSR/forms without an endpoint bundle → after gate A:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

Outputs: `forms.txt`, `links.txt`, `api_inline.txt`. If forms are approximately zero on a SPA, switch to Phase 1.

## Phase 1 — Static

Follow the gates and output limits.

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

Harvest parses HTML scripts and webpack/Vite manifests, downloads lazy chunks, and produces `js/`, `api_static.txt`, `routes.txt`, and `chunkmap.txt`.

Compare chunk counts with the manifest. Fix harvest for 404s rather than curling individual chunks. If API output is sparse, widen the OUTDIR endpoint regex and rerun (see reference).

### Phase 1b — Parameter reverse engineering

Paths come from Phase 1; fields require separate reconnaissance. For important APIs, establish field names, locations, inferred types, required status, sample values, and confidence.

#### 1b.0 Transport

| Form | Parameter location | Static evidence |
| --- | --- | --- |
| REST JSON | Body/query | `(params\|data\|body)\s*:\s*\{` near a path anchor |
| GraphQL | `variables` | gql templates and `$page: Int` |
| Traditional forms | URL-encoded | `<form>`, `FormData` |
| Uploads | Multipart | `FormData.append` |
| Path parameters | `/user/:id` | Routes, `useParams`, `$route.params` |
| Encrypted/signed | `sign`/`data` wrappers | Hook encryption-function arguments; reference D |

Annotate each API with `transport: query|json|form|graphql|encrypted`.

#### 1b.1 Anchored context windows

Use known paths to locate payload construction:

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

Inspect axios data/params, request interceptors' injected fields, generated OpenAPI method signatures, React Query/SWR hook arguments, and Vue composable arguments. Validation clues include yup/zod/rules, `Form.Item name=`, and embedded Swagger.

Output `param_candidates.json`: `{ path, fields[], source: "static-callsite", confidence }`.

#### 1b.2 Binding layer

Trace `Form field → onFinish/handleSubmit → transform → API payload`.

- Forms: submit → transform → API.
- Table search: `getFieldsValue()` → `params`.
- Routes: `:id` / `?tab=`.
- Interceptors: global tenantId/pagination/sign.
- Select options: UI options → API enum values.

Trace DevTools call stacks upward from fetch/XHR.send to payload construction.

#### 1b.3 Three payload questions

These differ from Phase 2's authentication gates:

| Question | Evidence |
| --- | --- |
| Assembly | Where payload is built/transformed |
| Validation | required/pattern/enum |
| Transport | path/query/body/multipart/headers |

Also inspect interceptor-injected Authorization, X-Tenant-Id, and signatures.

#### 1b.4 Runtime handoff

Static/binding evidence gives candidates. Required/optional/conditional status needs the Phase 3 matrix, sample diffs, and Phase 5 error inference.

## Phase 2 — Three authentication gates

Search OUTDIR/js with bounded grep output and record conclusions in config.json (recipes in reference):

| Gate | Question | Keywords |
| --- | --- | --- |
| Rendering | What determines logged-in state? | isLogin, getToken, cookies/localStorage |
| Interceptor | What redirects to /login? | response_code, errno, axios interceptors |
| Content | Where do menus/permissions originate? | menu, permission, role, acl, routes |

A storage key name is not a credential; confirm it through chunks/request chains. Exit through gate B: save config.json and adapt runtime_harvest.js/preload.js.

### Phase 2b — Optional API observation

Use OUTDIR/preload.js to confirm session keys, Authorization, and nested API URLs:

| Configuration | Output |
| --- | --- |
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | Observed headers |
| `extractUrlsFromResponse: true` | APIs embedded in responses |
| `observe.storageReads/cookieReads: true` | Configuration evidence |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

Export `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, and `__API_RECON_OBSERVE__` every coverage round.

## Phase 3 — Runtime

Gate B must be complete. Respect the reconnaissance boundary and credential-free mock strategy. Set `runtimeMode` to depth, coverage, or both in config.json (reference templates).

### Shared hooks and stubs

| Layer | Scope | Purpose |
| --- | --- | --- |
| L1 exact | Auth/permission/bootstrap stubs | Pass initial rendering gates |
| L2 negative-code correction | JSON responses | Replace logged-out business codes with success |
| L3 fallback | Unmatched /api calls | Empty success bodies to render the UI |

- Depth: fake auth, forward business-code patching, stubs, and route traversal (hash/history) → runtime_api.json.
- Coverage: inject preload.js at document-start using CDP addScriptToEvaluateOnNewDocument or a Userscript.

Verify `window.__API_RECON_PRELOAD__` exists and business paths do not return to /login.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

TLS certificate verification and the Chromium sandbox are enabled by default. Set `caCert` for private CAs; Python uses `ARTEX_RECON_CA_CERT`. Only explicitly authorized isolated targets may use `insecureTLS: true`, `allowNoSandbox: true` (JS), or `ARTEX_RECON_INSECURE_TLS=1` (Python). Running as root without explicit allowNoSandbox causes browser startup to be refused.

### 3b — Mandatory coverage enumeration

1. Click each main-navigation/sidebar item and allow 1–3 seconds for network activity.
2. Visit tabs (`role=tab`, `.ant-tabs-tab`).
3. Open the first table row's view/edit/details actions.
4. Inspect toolbar export/filter/create actions while avoiding irreversible deletion.
5. Merge APIs/routes after each module.
6. For SPA only, use controlled pushState for uncovered routes.txt paths; never use it for MPA.

Capture each operation type per module and diff multiple samples:

| Operation | Typical additional fields |
| --- | --- |
| Initial list | Pagination/default filters |
| Search | keyword/filter |
| Advanced filters | More optional fields |
| Create/edit | Complete entity |
| Batch/export/sort | ids[], exportType, sortField |

Outbound bodies/headers remain real even when responses are stubbed; use requests as evidence. Save scan_raw.json, param_samples.json, and api_detail.json.

Vue uses neutralizeVueRouter plus document-start preload. React uses routes.txt, sidebar clicks, and pushState. Both mode runs depth before coverage.

## Phase 4 — Restore the permission tree

Run when module pages are blank or every route emits only bootstrap/locale requests: the content gate remains closed.

| Symptom | Meaning |
| --- | --- |
| Post-login shell renders | Rendering/interceptor gates passed |
| Missing sidebar/blank module | Incomplete stub shape or permission codes |
| Same few APIs on every route | Permission v-if gate not passed |
| Too few routes vs bundles | Recover missing routes from auth modules |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Typical chain: role_permissions flat codes + permissions/all tree → getResultTree → userRouteAuth[CODE].url.

Outputs: route_map.json, userRouteAuth.json, permissions_tree.json, *_stub.json, perm_codes_all.txt. Match outer response_code to the interceptor gate, align flat codes/tree, and cover all route_map links in routes. Update config.json and rerun Phase 3. Large SPAs may require waitUntil/routeTimeout/perRouteMs tuning (reference A3/I).

## Phase 5 — Merge and report

| Output | Phase | Contents |
| --- | --- | --- |
| js/, api_static.txt, routes.txt, chunkmap.txt | 1 | Static bundles/paths |
| param_candidates.json | 1b | Candidate static fields |
| config.json | 2 | Three gates/runtime settings |
| runtime_api.json | 3a | Detailed depth recordings, including WS/SSE |
| param_samples.json, scan_raw.json, api_detail.json | 3b | Samples/click log/details |
| route_map.json and related files | 4 | Permission reconstruction, if needed |
| params_merged.json | 5 | Merged fields/confidence |
| api_merged.txt | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| site_map.json | 5 | Routes/APIs/parameters/features/limitations |
| insert_assets | 5 | Every discovered service and endpoint in the asset database |

### 5b Parameter merge

Diff param_samples.json; there is no universal merge script. Use reference J7 confidence levels: high/medium/low/pending trigger.

### 5c Error-based inference

Within authorization, incomplete requests may reveal parameter requirements through HTTP 400 (`field 'x' is required`, enum errors). This is parameter reconnaissance, not vulnerability testing. Account for data wrappers, variables, and pre-encryption bizData.

Report runtimeMode, static/runtime API counts, parameter confidence, uncovered modules, and a summary of CHANGES.md relative to reference scripts.

Suggested site_map.json:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

## General notes

- Framework independent: webpack/Vite/Angular lazy-loading follows the same approach.
- Transports: REST/JSON, GraphQL, WebSocket, SSE; gRPC-web is outside scope.
- SSR: client fetch can be recorded; RSC/Server Actions cannot be fully enumerated.
- JSVMP/WASM/strong HMAC or mTLS validation may require static-only results and explicit limitations.
- Conditional/hidden/WASM-built parameters remain pending trigger or unreachable when unobserved.
- Static discovery remains a fallback when runtime is blocked.

See [reference.md](reference.md) for grep recipes, configuration templates, troubleshooting, hooks, parameter methodology, and site-map fields. Reference script locations are listed under Scripts and gates.
